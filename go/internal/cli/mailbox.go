package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/cmdref"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/nodefs"
)

// Mailbox implements `orch mailbox post|list|ack|reply|inbox`, byte-identical to
// bin/mailbox.mjs. Where the JS dies of an uncaught exception, Mailbox prints only the error
// line and exits 1 (see crash).
func Mailbox(args []string, stdio Stdio) int {
	args = decodeArgv(args)
	parsed := cliargs.ParseMailbox(args)
	root, err := parsed.ProjectRoot(os.Getenv)
	if err != nil {
		return fail(stdio, "mailbox", err)
	}
	box := mailbox.New(root)
	switch parsed.Command {
	case "post":
		return mailboxPost(box, parsed, stdio)
	case "list":
		return mailboxList(box, parsed, stdio)
	case "reply":
		return mailboxReply(box, flipcmd.NewProject(root), args[1:], stdio)
	case "inbox":
		return mailboxInbox(box, stdio)
	case "ack":
		return mailboxAck(box, parsed, stdio)
	}
	fmt.Fprintln(stdio.Err, "commands: post | list | ack | reply | inbox")
	return 2
}

// mailboxPost prints {id, at}, or with --json the whole message (an orch addition; the JS
// ignores --json). As in list, only a bare --json counts.
func mailboxPost(box *mailbox.Mailbox, parsed cliargs.MailboxArgs, stdio Stdio) int {
	body := parsed.Values["body"]
	if body == "-" {
		var err error
		if body, err = cliargs.ReadStdinText(stdio.In); err != nil {
			return crash(stdio, err)
		}
	}
	msg, err := box.Post(mailbox.Draft{
		From:    parsed.Values["from"],
		To:      valueOr(parsed.Values["to"], "*"),
		Kind:    valueOr(parsed.Values["kind"], "note"),
		Subject: parsed.Values["subject"],
		Body:    body,
		Refs:    parsed.Refs,
		Thread:  parsed.Values["thread"],
	})
	if err != nil {
		return crash(stdio, err)
	}
	if parsed.Flags["json"] {
		return printLine(stdio, msg)
	}
	receipt := jsjson.NewObject()
	receipt.Set("id", jsjson.Path(msg, "id"))
	receipt.Set("at", jsjson.Path(msg, "at"))
	return printLine(stdio, receipt)
}

func mailboxList(box *mailbox.Mailbox, parsed cliargs.MailboxArgs, stdio Stdio) int {
	msgs, err := box.List(mailbox.Query{
		To:        parsed.Values["to"],
		Kind:      parsed.Values["kind"],
		Since:     parsed.Values["since"],
		UnackedBy: parsed.Values["unacked-by"],
		Limit:     jsjson.ToNumber(valueOr(parsed.Values["limit"], "50")),
	})
	if err != nil {
		return crash(stdio, err)
	}
	if parsed.Flags["json"] {
		items := make([]any, len(msgs))
		for i, m := range msgs {
			items[i] = m
		}
		data, err := jsjson.MarshalIndent(items, "  ")
		if err != nil {
			return crash(stdio, err)
		}
		fmt.Fprintf(stdio.Out, "%s\n", data)
		return 0
	}
	for _, m := range msgs {
		line, err := listLine(m)
		if err != nil {
			return crash(stdio, err)
		}
		fmt.Fprintln(stdio.Out, line)
	}
	return 0
}

// listLine is the text list entry: a header line, then the refs and up to six body lines.
func listLine(m *jsjson.Object) (string, error) {
	kind, isString := jsjson.Path(m, "kind").(string)
	if !isString {
		return "", &mailbox.ShapeError{Detail: "m.kind.padEnd is not a function"}
	}
	id8, err := sliceText(m, "id", 8)
	if err != nil {
		return "", err
	}
	line := text(m, "at") + "  " + jsstr.PadEnd(kind, 8) + " " + text(m, "from") + " -> " + text(m, "to") + "  " + text(m, "subject") + "  [" + id8 + "]"
	if ackedBy, _ := jsjson.Path(m, "acked_by").([]any); len(ackedBy) > 0 {
		line += "  acked:" + jsjson.Join(ackedBy, ",")
	}
	refs, err := refsText(m)
	if err != nil {
		return "", err
	}
	line += refs
	if body := jsjson.Path(m, "body"); jsjson.Truthy(body) {
		s, isString := body.(string)
		if !isString {
			return "", &mailbox.ShapeError{Detail: "m.body.split is not a function"}
		}
		lines := strings.Split(s, "\n")
		line += "\n    " + strings.Join(lines[:min(len(lines), 6)], "\n    ")
	}
	return line, nil
}

// refsText returns the conditional refs suffix, including its label even when join is empty.
// It mirrors the JS for whatever refs holds: null and undefined throw, a non-empty string or an
// object with a truthy length throws because it has no join, and numbers and booleans have no
// length, so they print nothing.
func refsText(m *jsjson.Object) (string, error) {
	refs, present := jsjson.Lookup(m, "refs")
	switch refs := refs.(type) {
	case nil:
		return "", &mailbox.ShapeError{Detail: "Cannot read properties of " + jsjson.TemplateStringOf(refs, present) + " (reading 'length')"}
	case []any:
		if len(refs) > 0 {
			return "\n    refs: " + jsjson.Join(refs, ", "), nil
		}
	case string:
		if refs != "" {
			return "", &mailbox.ShapeError{Detail: "m.refs.join is not a function"}
		}
	case *jsjson.Object:
		if jsjson.Truthy(jsjson.Path(refs, "length")) {
			return "", &mailbox.ShapeError{Detail: "m.refs.join is not a function"}
		}
	}
	return "", nil
}

// mailboxReply posts the human's answer to a message as a threaded note to its sender and
// acknowledges the message; a reply to the human gate's question also clears the gate. The id
// is the first token not starting with "--", so an option's value counts (`reply --root /x
// abc hi` takes /x as the id), as in the JS.
func mailboxReply(box *mailbox.Mailbox, project flipcmd.Project, rest []string, stdio Stdio) int {
	var positional []string
	for _, arg := range rest {
		if !strings.HasPrefix(arg, "--") {
			positional = append(positional, arg)
		}
	}
	idArg, body := "", ""
	if len(positional) > 0 {
		idArg, body = positional[0], strings.Join(positional[1:], " ")
	}
	if body == "" && !cliargs.IsTerminal(stdio.In) {
		var err error
		if body, err = cliargs.ReadStdinText(stdio.In); err != nil {
			return crash(stdio, err)
		}
	}
	if idArg == "" || body == "" {
		fmt.Fprintln(stdio.Err, `usage: mailbox.mjs reply <id-or-prefix> "your answer"`)
		return 2
	}
	id, err := box.ResolveID(idArg)
	if err != nil {
		return crash(stdio, err)
	}
	all, err := box.List(mailbox.Query{Limit: 100000})
	if err != nil {
		return crash(stdio, err)
	}
	var orig *jsjson.Object
	for _, m := range all {
		if s, _ := jsjson.Path(m, "id").(string); s == id {
			orig = m
			break
		}
	}
	var to any = "*"
	subject := id
	if orig != nil {
		if from := jsjson.Path(orig, "from"); jsjson.Truthy(from) {
			to = from
		}
		subject = jsjson.OrText(jsjson.Path(orig, "subject"), id)
	}
	reply, err := box.Post(mailbox.Draft{From: "human", To: to, Kind: "note", Subject: jsstr.Slice("Re: "+subject, 0, 140), Body: body, Thread: id})
	if err != nil {
		return crash(stdio, err)
	}
	human := "human"
	if _, err := box.Ack(mailbox.AckRequest{ID: &id, By: &human, Note: "replied"}); err != nil {
		return crash(stdio, err)
	}
	replyID, _ := jsjson.Path(reply, "id").(string)
	fmt.Fprintf(stdio.Out, "replied (%s) and acknowledged %s; the conductor reads it on its next turn\n", jsstr.Slice(replyID, 0, 8), jsstr.Slice(id, 0, 8))
	clearGateAnsweredBy(box, project, id, stdio)
	return 0
}

// clearGateAnsweredBy clears the human gate when id answers it: the gate's question (stored as
// a full id or a prefix) resolves to that same message, the way the reply's own id was
// resolved. A question that resolves to no single message is reported and answers nothing.
// Clearing queues the conductor's relaunch. The reply has already succeeded, so a failure here
// is reported on stderr and leaves the exit code alone.
func clearGateAnsweredBy(box *mailbox.Mailbox, project flipcmd.Project, id string, stdio Stdio) {
	record := readFlipForGate(project, stdio)
	question, _ := jsjson.Path(record, "awaiting_human", "question").(string)
	if question == "" {
		return
	}
	gateID, err := box.ResolveID(question)
	if err != nil {
		fmt.Fprintf(stdio.Err, "could not match the human gate's question: %s\n", err)
		return
	}
	if gateID != id {
		return
	}
	door, queued, err := project.ClearGate(record, true)
	if err != nil {
		fmt.Fprintf(stdio.Err, "could not clear the human gate: %s\n", err)
		return
	}
	if !queued {
		fmt.Fprintln(stdio.Out, "cleared the human gate")
		return
	}
	fmt.Fprintf(stdio.Out, "cleared the human gate; the supervisor relaunches the %s conductor within a minute\n", jsjson.TemplateString(door))
}

// readFlipForGate is flip.json, or nil when there is none. A file that cannot be read or
// parsed is reported, not skipped silently.
func readFlipForGate(project flipcmd.Project, stdio Stdio) any {
	data, err := nodefs.ReadFile(project.FlipPath)
	if err != nil {
		if nodefs.Code(err) != "ENOENT" {
			fmt.Fprintf(stdio.Err, "could not check for a human gate: %s\n", err)
		}
		return nil
	}
	record, err := jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		fmt.Fprintln(stdio.Err, "could not check for a human gate: .orchestrator/flip.json is not valid JSON")
		return nil
	}
	return record
}

// mailboxInbox lists the open questions for the human. The limit applies before the
// recipient filter, as in the JS: questions to others among the latest 50 hide older ones.
func mailboxInbox(box *mailbox.Mailbox, stdio Stdio) int {
	msgs, err := box.List(mailbox.Query{Kind: "question", UnackedBy: "human", Limit: 50})
	if err != nil {
		return crash(stdio, err)
	}
	var questions []*jsjson.Object
	for _, m := range msgs {
		if to, _ := jsjson.Path(m, "to").(string); to == "human" || to == "*" {
			questions = append(questions, m)
		}
	}
	if len(questions) == 0 {
		fmt.Fprintln(stdio.Out, "No open questions for you.")
		return 0
	}
	reply := cmdref.MailboxReply(cmdref.OrchOnPath())
	for _, m := range questions {
		id8, err := sliceText(m, "id", 8)
		if err != nil {
			return crash(stdio, err)
		}
		at16, err := sliceText(m, "at", 16)
		if err != nil {
			return crash(stdio, err)
		}
		body := strings.ReplaceAll(jsstr.Slice(text(m, "body"), 0, 400), "\n", " ")
		fmt.Fprintf(stdio.Out, "[%s] %sZ from %s: %s\n    %s\n    reply: %s %s \"your answer\"\n\n",
			id8, at16, text(m, "from"), text(m, "subject"), body, reply, id8)
	}
	return 0
}

func mailboxAck(box *mailbox.Mailbox, parsed cliargs.MailboxArgs, stdio Stdio) int {
	entry, err := box.Ack(mailbox.AckRequest{ID: optional(parsed.Values, "id"), By: optional(parsed.Values, "by"), Note: parsed.Values["note"]})
	if err != nil {
		return crash(stdio, err)
	}
	return printLine(stdio, entry)
}

// text is `${m[key]}`.
func text(m *jsjson.Object, key string) string {
	return jsjson.TemplateStringOf(jsjson.Lookup(m, key))
}

// sliceText is `${m[key].slice(0, end)}`: strings slice by UTF-16 code units, arrays by
// elements (then join with ","); other values have no slice and throw.
func sliceText(m *jsjson.Object, key string, end int) (string, error) {
	v, present := jsjson.Lookup(m, key)
	switch v := v.(type) {
	case string:
		return jsstr.Slice(v, 0, end), nil
	case []any:
		return jsjson.TemplateString(v[:min(len(v), end)]), nil
	case nil:
		return "", &mailbox.ShapeError{Detail: "Cannot read properties of " + jsjson.TemplateStringOf(v, present) + " (reading 'slice')"}
	}
	return "", &mailbox.ShapeError{Detail: "m." + key + ".slice is not a function"}
}

// valueOr is `opts[key] || fallback` for a string value.
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// optional is opts[key] where a missing key is undefined (nil).
func optional(values map[string]string, key string) *string {
	if value, ok := values[key]; ok {
		return &value
	}
	return nil
}
