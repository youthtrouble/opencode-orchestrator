// Package watch is bin/watch.mjs: a one-screen dashboard of a project (budgets, routing,
// supervisor, flip state, lanes, questions, mailbox, incidents, the next action). Every value
// is printed with the JS semantics of the template it appears in, and every TypeError the JS
// would throw on an unexpected shape is returned with V8's message.
package watch

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/routing"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

// Deps is what a render reads besides the project's files.
type Deps struct {
	Root    string // the project directory
	Home    string // the home holding the incident log
	Now     func() time.Time
	Routing func() (*usage.Routing, error) // getRouting({})
	Alive   func(pid any) bool
	// OrchOnPath picks the reply hint's form (cmdref.MailboxReply, PE-26); nil means false.
	OrchOnPath func() bool
}

// RoutingError is a failed getRouting, which the CLI reports as `orch watch: <error>` (PE-2)
// where the JS dies of the rejection.
type RoutingError struct{ Err error }

func (e *RoutingError) Error() string { return e.Err.Error() }
func (e *RoutingError) Unwrap() error { return e.Err }

// Render is watch.mjs's render(): the dashboard lines joined by "\n".
func Render(d Deps) (string, error) {
	r, err := d.Routing()
	if err != nil {
		return "", &RoutingError{err}
	}
	flip := nodefs.ReadJSONOrNull(filepath.Join(d.Root, ".orchestrator", "flip.json"))
	steps := []func() ([]string, error){
		func() ([]string, error) { return header(d, r) },
		func() ([]string, error) { return promptHead(r), nil },
		func() ([]string, error) { return routingLine(r) },
		func() ([]string, error) { return supervisorLine(d), nil },
		func() ([]string, error) { return flipLine(flip) },
		func() ([]string, error) { return lanesLine(d) },
		func() ([]string, error) { return questions(d) },
		func() ([]string, error) { return awaitingHuman(flip), nil },
		func() ([]string, error) { return recentMail(d) },
		func() ([]string, error) { return incidents(d) },
		func() ([]string, error) { return nextAction(d) },
	}
	var out []string
	for _, step := range steps {
		lines, err := step()
		if err != nil {
			return "", err
		}
		out = append(out, lines...)
	}
	return strings.Join(out, "\n"), nil
}

func header(d Deps, r *usage.Routing) ([]string, error) {
	generated, present := jsjson.Lookup(r.Usage, "generated_at")
	at, err := slice(generated, present, "r.usage.generated_at", 11, 19)
	if err != nil {
		return nil, err
	}
	stale := ""
	if jsjson.Truthy(jsjson.Path(r.Usage, "stale")) {
		stale = " STALE"
	}
	clock := jsstr.Slice(jsjson.ISOString(d.Now()), 0, 19)
	return []string{"== " + filepath.Base(d.Root) + "  " + clock + "Z  (usage from " + at + "Z" + stale + ")"}, nil
}

func promptHead(r *usage.Routing) []string {
	lines := strings.Split(r.Prompt, "\n")
	return lines[:min(2, len(lines))]
}

// routingLine is `role=<model after "/">` plus `@effort` or else `#variant`, or `role=PAUSED`,
// joined by two spaces.
func routingLine(r *usage.Routing) ([]string, error) {
	parts := make([]string, 0, len(r.Resolved.Roles))
	for _, a := range r.Resolved.Roles {
		if a.Entry == nil {
			parts = append(parts, a.Role+"=PAUSED")
			continue
		}
		model, err := routing.ModelName(r.Registry, a.Entry)
		if err != nil {
			return nil, err
		}
		suffix := ""
		if jsjson.Truthy(a.Entry.Effort) {
			suffix = "@" + jsjson.TemplateString(a.Entry.Effort)
		} else if jsjson.Truthy(a.Entry.Variant) {
			suffix = "#" + jsjson.TemplateString(a.Entry.Variant)
		}
		parts = append(parts, a.Role+"="+model+suffix)
	}
	return []string{"routing: " + strings.Join(parts, "  ")}, nil
}

func supervisorLine(d Deps) []string {
	sup := nodefs.ReadJSONOrNull(filepath.Join(d.Root, ".orchestrator", "supervisor-state.json"))
	if !jsjson.Truthy(sup) {
		return []string{"supervisor: not started"}
	}
	dead := ""
	if !d.Alive(jsjson.Path(sup, "pid")) {
		dead = " (DEAD)"
	}
	checked := jsstr.Slice(orEmpty(jsjson.Path(sup, "checked_at")), 11, 19)
	return []string{"supervisor: " + field(sup, "status") + " pid " + field(sup, "pid") + dead + " checked " + checked + "Z"}
}

// flipLine throws, as the JS does, for a flip.json that is neither paused nor holds `at`: the
// state `flip gate` leaves on a project that never flipped (plan 3.4 defect 1, mirrored).
func flipLine(flip any) ([]string, error) {
	if !jsjson.Truthy(flip) {
		return []string{"flip: never"}, nil
	}
	if paused := jsjson.Path(flip, "paused"); jsjson.Truthy(paused) {
		atValue, present := jsjson.Lookup(paused, "at")
		at, err := slice(atValue, present, "flip.paused.at", 11, 16)
		if err != nil {
			return nil, err
		}
		return []string{"flip: PAUSED (" + field(paused, "door") + ", " + at + "Z)"}, nil
	}
	atValue, present := jsjson.Lookup(flip, "at")
	at, err := slice(atValue, present, "flip.at", 11, 16)
	if err != nil {
		return nil, err
	}
	return []string{"flip: " + field(flip, "from") + "->" + field(flip, "to") + " at " + at + "Z"}, nil
}

func lanesLine(d Deps) ([]string, error) {
	project := worker.NewProject(d.Root)
	names, err := project.Lanes()
	if err != nil {
		return nil, err
	}
	var lanes []string
	for _, name := range names {
		l := project.ReadState(name)
		if !jsjson.Truthy(l) {
			continue
		}
		status := jsjson.Path(l, "status")
		dead := ""
		if (status == "running" || status == "starting") && !d.Alive(jsjson.Path(l, "pid")) {
			dead = " (DEAD)"
		}
		lastTool := ""
		if tool := jsjson.Path(l, "last_tool"); jsjson.Truthy(tool) {
			lastTool = "last " + jsjson.TemplateString(tool)
		}
		progress := jsjson.Path(l, "last_progress_at")
		if !jsjson.Truthy(progress) {
			progress = jsjson.Path(l, "updated_at")
		}
		lanes = append(lanes, field(l, "lane")+"["+field(l, "harness")+"] "+field(l, "status")+dead+" "+lastTool+" "+jsstr.Slice(orEmpty(progress), 11, 16)+"Z")
	}
	if len(lanes) == 0 {
		return []string{"lanes: none"}, nil
	}
	return []string{"lanes: " + strings.Join(lanes, " | ")}, nil
}

func questions(d Deps) ([]string, error) {
	msgs, err := mailbox.New(d.Root).List(mailbox.Query{Kind: "question", UnackedBy: "human", Limit: 20})
	if err != nil {
		return nil, err
	}
	var forHuman []*jsjson.Object
	for _, m := range msgs {
		if to := jsjson.Path(m, "to"); to == "human" || to == "*" {
			forHuman = append(forHuman, m)
		}
	}
	if len(forHuman) == 0 {
		return nil, nil
	}
	reply := cmdref.MailboxReply(d.OrchOnPath != nil && d.OrchOnPath())
	out := []string{fmt.Sprintf("QUESTIONS FOR YOU (%d):", len(forHuman))}
	for _, q := range forHuman {
		idValue, idPresent := jsjson.Lookup(q, "id")
		id, err := slice(idValue, idPresent, "q.id", 0, 8)
		if err != nil {
			return nil, err
		}
		subjectValue, subjectPresent := jsjson.Lookup(q, "subject")
		subject, err := slice(subjectValue, subjectPresent, "q.subject", 0, 100)
		if err != nil {
			return nil, err
		}
		out = append(out, "  ["+id+"] "+subject+"\n     reply: "+reply+" "+id+` "..."`)
	}
	return out, nil
}

func awaitingHuman(flip any) []string {
	gate := jsjson.Path(flip, "awaiting_human")
	if !jsjson.Truthy(gate) {
		return nil
	}
	return []string{"AWAITING HUMAN: " + field(gate, "reason") + "  (clear: " + cmdref.FlipGateClear() + ")"}
}

func recentMail(d Deps) ([]string, error) {
	msgs, err := mailbox.New(d.Root).List(mailbox.Query{Limit: 8})
	if err != nil {
		return nil, err
	}
	out := []string{fmt.Sprintf("mailbox (last %d):", len(msgs))}
	for _, m := range msgs {
		atValue, atPresent := jsjson.Lookup(m, "at")
		at, err := slice(atValue, atPresent, "m.at", 11, 16)
		if err != nil {
			return nil, err
		}
		kindValue, kindPresent := jsjson.Lookup(m, "kind")
		kind, err := padEnd(kindValue, kindPresent, "m.kind", 8)
		if err != nil {
			return nil, err
		}
		out = append(out, "  "+at+"Z "+kind+" "+field(m, "from")+" -> "+field(m, "to")+": "+field(m, "subject"))
	}
	return out, nil
}

func incidents(d Deps) ([]string, error) {
	since := jsjson.ISOString(d.Now().Add(-24 * time.Hour))
	entries, err := incident.New(d.Home).Read(incident.Filter{Since: since})
	if err != nil {
		return nil, err
	}
	line := fmt.Sprintf("incidents (24h): %d", len(entries))
	if len(entries) > 0 {
		latest := entries[len(entries)-1]
		if latest == nil {
			return nil, jsjson.CannotReadOfNull("summary")
		}
		summaryValue, present := jsjson.Lookup(latest, "summary")
		summary, err := slice(summaryValue, present, "inc[inc.length - 1].summary", 0, 90)
		if err != nil {
			return nil, err
		}
		line += "  latest: " + summary
	}
	return []string{line}, nil
}

// nextAction is `text.split("## Next action")[1]?.split("##")[0]?.trim().split("\n")[0]`: the
// rest of the heading line, so `## Next action (exact)` yields `(exact)` (plan 3.4 defect 5,
// mirrored).
func nextAction(d Deps) ([]string, error) {
	state := filepath.Join(d.Root, ".orchestrator", "STATE.md")
	if !nodefs.Exists(state) {
		return nil, nil
	}
	data, err := nodefs.ReadFile(state)
	if err != nil {
		return nil, err
	}
	_, after, found := strings.Cut(jsstr.DecodeUTF8(data), "## Next action")
	if !found {
		return nil, nil
	}
	section, _, _ := strings.Cut(after, "##")
	next, _, _ := strings.Cut(strings.TrimFunc(section, isJSWhitespace), "\n")
	if next == "" {
		return nil, nil
	}
	return []string{"next: " + jsstr.Slice(next, 0, 160)}, nil
}

// isJSWhitespace is the set String.prototype.trim removes: WhiteSpace and LineTerminator.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// field is `${v.key}`.
func field(v any, key string) string {
	value, present := jsjson.Lookup(v, key)
	return jsjson.TemplateStringOf(value, present)
}

// orEmpty is `String(v || "")`.
func orEmpty(v any) string {
	if !jsjson.Truthy(v) {
		return ""
	}
	return jsjson.TemplateString(v)
}

// slice is `<expr>.slice(start, end)` on a value read with Lookup.
func slice(v any, present bool, expr string, start, end int) (string, error) {
	switch v := v.(type) {
	case string:
		return jsstr.Slice(v, start, end), nil
	case []any: // Array.prototype.slice, then String() of the result
		from, to := relative(start, len(v)), relative(end, len(v))
		return jsjson.Join(v[from:max(from, to)], ","), nil
	}
	return "", methodError(v, present, expr, "slice")
}

// padEnd is `<expr>.padEnd(length)` on a value read with Lookup.
func padEnd(v any, present bool, expr string, length int) (string, error) {
	if s, ok := v.(string); ok {
		return jsstr.PadEnd(s, length), nil
	}
	return "", methodError(v, present, expr, "padEnd")
}

// methodError is the TypeError of calling a string method on a value that lacks it.
func methodError(v any, present bool, expr, method string) error {
	switch {
	case !present:
		return &jsjson.TypeError{Message: "Cannot read properties of undefined (reading '" + method + "')"}
	case v == nil:
		return jsjson.CannotReadOfNull(method)
	}
	return &jsjson.TypeError{Message: expr + "." + method + " is not a function"}
}

// relative is an Array.prototype.slice index: negative counts from the end, clamped to
// [0, length].
func relative(i, length int) int {
	if i < 0 {
		return max(length+i, 0)
	}
	return min(i, length)
}

// FollowDeps drives the --follow loop.
type FollowDeps struct {
	Render   func() (string, error)
	Interval any // opts.interval: absent (nil), a bare flag (true) or the given text
	Out, Err io.Writer
	Sleep    func(ctx context.Context, d time.Duration)
}

// Follow is the --follow loop: clear the screen and draw a frame, or print the error message,
// then wait `Number(opts.interval || 20) * 1000` ms (Node's timer clamp), until ctx ends. The
// CLI never ends it: like the JS, it runs until a signal kills the process.
func Follow(ctx context.Context, d FollowDeps) {
	delay := Delay(d.Interval)
	for ctx.Err() == nil {
		frame, err := d.Render()
		if err != nil {
			fmt.Fprintln(d.Err, err.Error())
		} else {
			fmt.Fprint(d.Out, "\x1b[2J\x1b[H"+frame+"\n")
		}
		d.Sleep(ctx, delay)
	}
}

// Delay is the wait between frames for opts.interval.
func Delay(interval any) time.Duration {
	seconds := 20.0
	if jsjson.Truthy(interval) {
		seconds = jsjson.ToNumber(interval)
	}
	return procs.TimerDelay(seconds * 1000)
}
