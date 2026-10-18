// Package cmdref holds the orchestrator command lines that are embedded in texts read by
// agents (supervisor reminders, briefs). While the JS runtime is installed alongside orch,
// these must stay byte-identical to the strings the JS writes; the switch to `orch <cmd>`
// happens in one place, here.
package cmdref

// Script is the command line that runs the named orchestrator script, e.g.
// Script("mailbox") is "node ~/.config/opencode/orchestrator/bin/mailbox.mjs".
func Script(name string) string {
	return "node ~/.config/opencode/orchestrator/bin/" + name + ".mjs"
}
