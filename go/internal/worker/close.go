package worker

import (
	"regexp"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/incident"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// Cuts of the texts a run's outcome is recorded with, in UTF-16 code units. A cut inside a
// surrogate pair keeps U+FFFD for the half (PE-1).
const (
	maxResultSummary   = 2000
	maxIncidentSummary = 160
	maxIncidentDetail  = 1500
	maxBlockedBody     = 1500
)

// exitBudgetRed is the bridges' exit code for a run the budget gate refused or a usage limit
// ended.
const exitBudgetRed = 75.0

// limitIgnoringCase is the JS /limit/i. A non-unicode /i folds ASCII letters only against
// ASCII letters, so the classes are spelled out (Go's (?i) would also fold non-ASCII letters).
var limitIgnoringCase = regexp.MustCompile(`[lL][iI][mM][iI][tT]`)

// parseResult is `JSON.parse(output.trim().split("\n").pop())`: the bridge's result is its last
// stdout line, which may be any JSON value; nil when it does not parse (or is null).
func parseResult(output string) any {
	trimmed := strings.Trim(output, jsstr.WhiteSpace)
	last := trimmed[strings.LastIndex(trimmed, "\n")+1:]
	result, err := jsjson.Unmarshal([]byte(last))
	if err != nil {
		return nil
	}
	return result
}

// closeWith takes the branch the bridge's close code and result call for: deferred for a
// budget refusal or a usage limit, finished for a result that is ok, failed otherwise. An error
// is the exception the JS dies of inside the branch, after what the branch did so far.
func (r *run) closeWith(code, result any) error {
	switch {
	case code == exitBudgetRed || jsjson.Path(result, "reason") == "budget_red":
		return r.deferred(result)
	case jsjson.Truthy(jsjson.Path(result, "ok")):
		return r.finished(code, result)
	}
	return r.failed(code, result)
}

// deferred publishes the deferral, records a limit incident and posts a deferred message.
func (r *run) deferred(result any) error {
	resetsAt := or(jsjson.Path(result, "resets_at"), nil)
	extra := jsjson.NewObject()
	extra.Set("deferred_reason", "budget_red")
	extra.Set("resume_after", resetsAt)
	extra.Set("result_summary", or(jsjson.Path(result, "note"), "budget gate refused"))
	r.finish("deferred", extra)

	lane, harness := jsjson.TemplateString(r.launch.lane), r.launch.harness
	context := jsjson.NewObject()
	context.Set("harness", harness)
	context.Set("resets_at", resetsAt)
	incident.TryRecord(incident.Draft{Component: "worker", Kind: "limit", Summary: "worker lane " + lane + " deferred: " + harness + " budget red", Context: context, Project: r.launch.project.Root})
	return r.post(mailbox.Draft{
		Kind:    "deferred",
		Subject: lane + " deferred: " + harness + " budget is red",
		Body:    "Resets " + jsjson.OrText(resetsAt, "unknown") + ". The supervisor will re-dispatch after reset if auto_redispatch is enabled.",
		Refs:    []string{},
	})
}

// finished publishes the finished run. It posts nothing: the agent posts its own release.
func (r *run) finished(code, result any) error {
	summary, err := cutSummary(jsjson.Path(result, "final_message"), "result.final_message")
	if err != nil {
		return err
	}
	sessionID, _ := r.state.Fields.Get("session_id")
	extra := jsjson.NewObject()
	extra.Set("session_id", or(jsjson.Path(result, "session_id"), or(jsjson.Path(result, "thread_id"), sessionID)))
	extra.Set("exit_code", code)
	extra.Set("result_summary", summary)
	// `record: result.record` with no record assigns undefined, which JSON leaves out; no
	// later publish sets the key, so leaving it unset writes the same file.
	if record, present := jsjson.Lookup(result, "record"); present {
		extra.Set("record", record)
	}
	extra.Set("usage", or(jsjson.Path(result, "usage"), nil))
	extra.Set("cost_usd", jsjson.Path(result, "cost_usd"))
	extra.Set("permission_denials", lengthOrZero(jsjson.Path(result, "permission_denials")))
	r.finish("finished", extra)
	return nil
}

// failed publishes the failure, records an incident (whose failure is ignored) and posts a
// blocked message.
func (r *run) failed(code, result any) error {
	summary, err := cutSummary(jsjson.Path(result, "final_message"), "(intermediate value)")
	if err != nil {
		return err
	}
	errs := jsjson.Path(result, "errors")
	record := jsjson.Path(result, "record")
	extra := jsjson.NewObject()
	extra.Set("exit_code", code)
	extra.Set("errors", or(errs, []any{"bridge produced no result"}))
	extra.Set("record", or(record, nil))
	extra.Set("result_summary", summary)
	r.finish("failed", extra)

	r.recordFailure(code, result)
	body, err := joinErrors(errs)
	if err != nil {
		return err
	}
	refs := []string{}
	if jsjson.Truthy(record) {
		ref, err := r.relativeRecord(record)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	lane := jsjson.TemplateString(r.launch.lane)
	return r.post(mailbox.Draft{Kind: "blocked", Subject: lane + " failed on " + r.launch.harness, Body: jsstr.Slice(body, 0, maxBlockedBody), Refs: refs})
}

// recordFailure is the failed run's incident: kind limit when String(result?.errors) mentions
// a limit, else crash. Errors that are not an array make the summary throw, which the JS
// catch swallows: no incident then.
func (r *run) recordFailure(code, result any) {
	errs, present := jsjson.Lookup(result, "errors")
	joined, err := joinErrors(errs)
	if err != nil {
		return
	}
	kind := "crash"
	if limitIgnoringCase.MatchString(jsjson.TemplateStringOf(errs, present)) {
		kind = "limit"
	}
	output := jsstr.DecodeUTF8(r.output)
	context := jsjson.NewObject()
	context.Set("harness", r.launch.harness)
	context.Set("model", r.launch.opts.Or("model", nil))
	context.Set("exit_code", code)
	incident.TryRecord(incident.Draft{
		Component: "worker",
		Kind:      kind,
		Summary:   "worker lane " + jsjson.TemplateString(r.launch.lane) + " failed on " + r.launch.harness + ": " + jsstr.Slice(joined, 0, maxIncidentSummary),
		Detail:    jsstr.Slice(output, -maxIncidentDetail, jsstr.Len(output)),
		Context:   context,
		Project:   r.launch.project.Root,
	})
}

// post sends the run's message from its lane to the coordinator. The JS does not catch a
// failure here.
func (r *run) post(draft mailbox.Draft) error {
	draft.From = r.launch.lane
	draft.To = "coordinator"
	_, err := mailbox.New(r.launch.project.Root).Post(draft)
	return err
}

// relativeRecord is path.relative(root, result.record), which throws for a record that is not
// a string.
func (r *run) relativeRecord(record any) (string, error) {
	path, isString := record.(string)
	if !isString {
		return "", &JSError{Class: "TypeError [ERR_INVALID_ARG_TYPE]", Message: `The "to" argument must be of type string. Received ` + describeReceived(record)}
	}
	return nodefs.Relative(r.launch.project.Root, path)
}

// describeReceived is how Node's ERR_INVALID_ARG_TYPE describes a non-string JSON value.
func describeReceived(v any) string {
	switch v := v.(type) {
	case bool:
		return "type boolean (" + jsjson.TemplateString(v) + ")"
	case float64:
		if v == 0 && 1/v < 0 {
			return "type number (-0)" // util.inspect keeps the sign String() drops
		}
		return "type number (" + jsjson.TemplateString(v) + ")"
	case []any:
		return "an instance of Array"
	}
	return "an instance of Object"
}

// cutSummary is `(message || "").slice(0, 2000)`: a string is cut, an array keeps its first
// 2000 elements, and any other truthy value has no slice method (a TypeError naming expr).
func cutSummary(message any, expr string) (any, error) {
	switch message := or(message, "").(type) {
	case string:
		return jsstr.Slice(message, 0, maxResultSummary), nil
	case []any:
		return append([]any{}, message[:min(len(message), maxResultSummary)]...), nil
	}
	return nil, &JSError{Class: "TypeError", Message: "(" + expr + ` || "").slice is not a function`}
}

// joinErrors is `(result?.errors || ["no result"]).join("; ")`, a TypeError for truthy errors
// that are not an array.
func joinErrors(errs any) (string, error) {
	list, isArray := or(errs, []any{"no result"}).([]any)
	if !isArray {
		return "", &JSError{Class: "TypeError", Message: `((intermediate value) || ["no result"]).join is not a function`}
	}
	return jsjson.Join(list, "; "), nil
}

// lengthOrZero is `v?.length ?? 0`: an array's length, a string's length in UTF-16 code units,
// an object's own length property, else 0.
func lengthOrZero(v any) any {
	switch v := v.(type) {
	case []any:
		return float64(len(v))
	case string:
		return float64(jsstr.Len(v))
	case *jsjson.Object:
		if length, _ := v.Get("length"); length != nil {
			return length
		}
	}
	return 0.0
}

// or is `v || fallback`.
func or(v, fallback any) any {
	if jsjson.Truthy(v) {
		return v
	}
	return fallback
}
