package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
)

// errCtxNotList is the JS TypeError of `record` after a trailing bare --ctx (opts.ctx is true).
var errCtxNotList = &jsTypeError{message: "opts.ctx.map is not a function"}

// listLimit is how many of the latest entries the text list prints.
const listLimit = 100

// Incident implements `orch incident record|list|report|share`, byte-identical to
// bin/incident.mjs except that entries carry orch's version (PE-4). Where the JS dies of an
// uncaught exception, Incident prints only the error line and exits 1 (see crash).
func Incident(args []string, stdio Stdio) int {
	parsed, err := cliargs.ParseIncident(decodeArgv(args))
	if err != nil {
		return crash(stdio, &jsTypeError{message: err.Error()})
	}
	home, err := paths.NodeHome(os.LookupEnv)
	if err != nil {
		return fail(stdio, "incident", err)
	}
	log := incident.New(home)
	switch parsed.Command {
	case "record":
		return incidentRecord(log, parsed, stdio)
	case "list":
		return incidentList(log, parsed.Opts, stdio)
	case "report":
		return incidentReport(log, parsed.Opts, stdio)
	case "share":
		return incidentShare(log, parsed.Opts, stdio)
	}
	fmt.Fprintf(stdio.Err, "commands: record | list | report | share   (log: %s)\n", log.File)
	return 2
}

// incidentRecord appends one incident and prints {recorded: fp, at}. Its checks run in the JS
// order: --detail - reads stdin first, then a bare --ctx fails, then a missing summary, then a
// bare --cwd. --request takes the incident from stdin instead (PE-25).
func incidentRecord(log *incident.Log, parsed cliargs.IncidentArgs, stdio Stdio) int {
	opts := parsed.Opts
	if _, present := opts["request"]; present {
		return incidentRecordRequest(log, parsed, stdio)
	}
	detail := textOr(opts, "detail", "")
	if detail == "-" {
		var err error
		if detail, err = cliargs.ReadStdinText(stdio.In); err != nil {
			return crash(stdio, err)
		}
	}
	if parsed.CtxFlag {
		return crash(stdio, errCtxNotList)
	}
	var component any = textOr(opts, "component", "agent")
	if opts.IsFlag("component") {
		component = true
	}
	draft := incident.Draft{
		Component: component,
		Kind:      textOr(opts, "kind", "note"),
		Summary:   textOr(opts, "summary", ""),
		Detail:    detail,
		Context:   contextOf(parsed.Ctx),
	}
	if err := draft.Validate(); err != nil {
		return crash(stdio, err)
	}
	if opts.IsFlag("cwd") {
		return crash(stdio, errPathArgBoolean)
	}
	draft.Project = textOr(opts, "cwd", "")
	return recordDraft(log, draft, stdio)
}

// recordDraft records a validated draft, its project defaulting to the working directory, and
// prints {recorded: fp, at}.
func recordDraft(log *incident.Log, draft incident.Draft, stdio Stdio) int {
	if draft.Project == "" {
		cwd, err := nodefs.Resolve(".")
		if err != nil {
			return crash(stdio, err)
		}
		draft.Project = cwd
	}
	entry, err := log.Record(draft)
	if err != nil {
		return crash(stdio, err)
	}
	receipt := jsjson.NewObject()
	receipt.Set("recorded", jsjson.Path(entry, "fp"))
	receipt.Set("at", jsjson.Path(entry, "at"))
	return printLine(stdio, receipt)
}

// requestConflicts are the record options --request replaces: the incident comes from stdin
// whole, or from argv, never from both.
var requestConflicts = []string{"component", "kind", "summary", "detail", "ctx", "cwd"}

// incidentRecordRequest is `record --request -` (PE-25; bin/incident.mjs has no such option):
// the incident is one JSON object on stdin, {component, kind, summary, detail, context,
// project}, so no free text reaches argv, where process listings show it before redaction.
// It builds the draft the flags would and records it the same way; a malformed request is a
// usage error (exit 2), a missing summary fails as it does with flags.
func incidentRecordRequest(log *incident.Log, parsed cliargs.IncidentArgs, stdio Stdio) int {
	if value, _ := parsed.Opts.Value("request"); value != "-" {
		return incidentUsage(stdio, "--request takes - (the incident as one JSON object on stdin)")
	}
	for _, key := range requestConflicts {
		_, present := parsed.Opts[key]
		if present || (key == "ctx" && (parsed.CtxFlag || len(parsed.Ctx) > 0)) {
			return incidentUsage(stdio, "--request cannot be combined with --"+key)
		}
	}
	text, err := cliargs.ReadStdinText(stdio.In)
	if err != nil {
		return crash(stdio, err)
	}
	draft, err := draftOfRequest(text)
	if err != nil {
		return incidentUsage(stdio, "--request: "+err.Error())
	}
	if err := draft.Validate(); err != nil {
		return crash(stdio, err)
	}
	return recordDraft(log, draft, stdio)
}

func incidentUsage(stdio Stdio, message string) int {
	fmt.Fprintln(stdio.Err, "orch incident record: "+message)
	return 2
}

// draftOfRequest parses a --request object. An absent, null or empty text field takes the
// flags' default; unknown keys and values of the wrong type are refused, so a caller's typo
// cannot silently drop a fact.
func draftOfRequest(text string) (incident.Draft, error) {
	value, err := jsjson.Unmarshal([]byte(text))
	if err != nil {
		return incident.Draft{}, fmt.Errorf("stdin is not JSON: %w", err)
	}
	request, isObject := value.(*jsjson.Object)
	if !isObject {
		return incident.Draft{}, fmt.Errorf("stdin must be a JSON object")
	}
	fields := map[string]string{"component": "agent", "kind": "note", "summary": "", "detail": "", "project": ""}
	draft := incident.Draft{Context: jsjson.NewObject()}
	for _, key := range request.Keys() {
		field, _ := request.Get(key)
		if key == "context" {
			if draft.Context, err = requestContext(field); err != nil {
				return incident.Draft{}, err
			}
			continue
		}
		if _, known := fields[key]; !known {
			return incident.Draft{}, fmt.Errorf("unknown key %q", key)
		}
		switch field := field.(type) {
		case nil:
		case string:
			if field != "" {
				fields[key] = field
			}
		default:
			return incident.Draft{}, fmt.Errorf("%s must be a string", key)
		}
	}
	draft.Component = fields["component"]
	draft.Kind = fields["kind"]
	draft.Summary = fields["summary"]
	draft.Detail = fields["detail"]
	draft.Project = fields["project"]
	return draft, nil
}

// requestContext is a --request context: null or an object of strings, numbers, booleans and
// nulls, whose strings the log redacts.
func requestContext(value any) (*jsjson.Object, error) {
	if value == nil {
		return jsjson.NewObject(), nil
	}
	context, isObject := value.(*jsjson.Object)
	if !isObject {
		return nil, fmt.Errorf("context must be an object")
	}
	for _, key := range context.Keys() {
		switch v, _ := context.Get(key); v.(type) {
		case nil, string, float64, bool:
		default:
			return nil, fmt.Errorf("context.%s must be a string, number, boolean or null", key)
		}
	}
	return context, nil
}

// textOr is `opts[key] || fallback` where the result is used as text: a bare flag (true)
// reads as "true".
func textOr(opts cliargs.Opts, key, fallback string) string {
	if opts.IsFlag(key) {
		return "true"
	}
	if value, _ := opts.Value(key); value != "" {
		return value
	}
	return fallback
}

// contextOf is Object.fromEntries(pairs.map((kv) => kv.split("="))): the key is the text before
// the first "=", the value the text up to the next one. A pair without "=" sets its key to
// undefined, which JSON omits but which keeps the key's position for a later value, so keys
// are placed by their first appearance and take their last value.
func contextOf(pairs []string) *jsjson.Object {
	var order []string
	values := map[string]*string{}
	for _, pair := range pairs {
		parts := strings.Split(pair, "=")
		key := parts[0]
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = nil
		if len(parts) > 1 {
			values[key] = &parts[1]
		}
	}
	context := jsjson.NewObject()
	for _, key := range order {
		if value := values[key]; value != nil {
			context.Set(key, *value)
		}
	}
	return context
}

// optionValue is opts[key] as a JS value: nil when absent, true for a bare flag, else the string.
func optionValue(opts cliargs.Opts, key string) any {
	if opts.IsFlag(key) {
		return true
	}
	if value, ok := opts.Value(key); ok {
		return value
	}
	return nil
}

func incidentList(log *incident.Log, opts cliargs.Opts, stdio Stdio) int {
	entries, err := log.Read(incident.Filter{
		Since:     optionValue(opts, "since"),
		Kind:      optionValue(opts, "kind"),
		Component: optionValue(opts, "component"),
	})
	if err != nil {
		return crash(stdio, err)
	}
	if opts.Truthy("json") {
		data, err := jsjson.MarshalIndent(entries, "  ")
		if err != nil {
			return crash(stdio, err)
		}
		fmt.Fprintf(stdio.Out, "%s\n", data)
		return 0
	}
	for _, e := range entries[max(len(entries)-listLimit, 0):] {
		fmt.Fprintln(stdio.Out, incident.ListLine(e))
	}
	return 0
}

// incidentReport prints the fingerprint groups, most frequent first: what the audit skill reads.
func incidentReport(log *incident.Log, opts cliargs.Opts, stdio Stdio) int {
	entries, err := log.Read(incident.Filter{Since: optionValue(opts, "since")})
	if err != nil {
		return crash(stdio, err)
	}
	summaries := incident.Summarize(entries)
	if len(summaries) == 0 {
		fmt.Fprintln(stdio.Out, "(no incidents)")
		return 0
	}
	for _, s := range summaries {
		line, err := s.ReportLine()
		if err != nil {
			return crash(stdio, err)
		}
		fmt.Fprintln(stdio.Out, line)
	}
	return 0
}

// incidentShare writes the sanitized bundle {generated_at, incidents, summary} (indent 2, no
// trailing newline) to --out, relative to the working directory. Entries lose their project
// key, even the basename; nothing is uploaded.
func incidentShare(log *incident.Log, opts cliargs.Opts, stdio Stdio) int {
	entries, err := log.Read(incident.Filter{Since: optionValue(opts, "since")})
	if err != nil {
		return crash(stdio, err)
	}
	shared := make([]any, len(entries))
	for i, e := range entries {
		withoutProject := jsjson.Spread(e) // ({project, ...e}) => e
		withoutProject.Delete("project")
		shared[i] = withoutProject
	}
	if opts.IsFlag("out") {
		return crash(stdio, errFileArgBoolean)
	}
	out := textOr(opts, "out", "incidents-share.json")
	summaries := incident.Summarize(shared)
	summary := make([]any, len(summaries))
	for i, s := range summaries {
		summary[i] = s.Object()
	}
	bundle := jsjson.NewObject()
	bundle.Set("generated_at", jsjson.ISOString(time.Now()))
	bundle.Set("incidents", shared)
	bundle.Set("summary", summary)
	data, err := jsjson.MarshalIndent(bundle, "  ")
	if err != nil {
		return crash(stdio, err)
	}
	if err := nodefs.WriteFile(out, data); err != nil {
		return crash(stdio, err)
	}
	fmt.Fprintf(stdio.Out, "wrote %s (%d incidents). Review it, then attach to a GitHub issue by hand. Nothing was uploaded.\n", out, len(shared))
	return 0
}
