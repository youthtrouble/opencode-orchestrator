// Package cmdref is the one place that spells the orchestrator command lines embedded in texts
// orch writes for agents and humans (supervisor reminders, the worker preamble, briefs, hints,
// usage lines and help). Every one reads `orch <cmd> ...`.
package cmdref

import "strings"

// Command is the command line that runs the named orch subcommand, e.g. Command("mailbox") is
// "orch mailbox".
func Command(name string) string {
	return "orch " + name
}

// MailboxReply is the command line `mailbox inbox` and `watch` tell the human to answer a
// question with, followed by the message id prefix and the answer.
func MailboxReply() string {
	return Command("mailbox") + " reply"
}

// FlipGateClear is the command line that clears a human gate, which `flip gate` tells the
// human to run.
func FlipGateClear() string {
	return Command("flip") + " gate --clear"
}

// WatchFollow is the command line kickoff tells the human to watch the run with.
func WatchFollow() string {
	return Command("watch") + " --follow"
}

// KickoffUsage is the usage line kickoff prints for --help or without a brief.
func KickoffUsage() string {
	return "usage: " + Command("kickoff") + " --brief FILE [--brief FILE2] [--door auto|claude|opencode] [--headless] [--unattended] [--dry-run]"
}

// KickoffPrompt is the conductor's opening prompt kickoff builds (bin/kickoff.mjs l.42): ship
// the main brief and, when there are side briefs, run them too as budget allows.
func KickoffPrompt(main string, side []string) string {
	sideText := ""
	if len(side) > 0 {
		sideText = " When budget allows, also run " + strings.Join(side, ", ") + " as side brief(s) with the cheapest adequate role."
	}
	return "Ship " + main + ". Plan first with the architect (size per the brief), then one slice at a time; flip or hand back per the contract at each brief boundary." +
		sideText + " Record orchestrator defects as incidents."
}

// ConductorContinueBrief is the brief `flip run` writes to .orchestrator/briefs/
// conductor-continue.md for the conductor it launches at door to (bin/flip.mjs l.118): from is
// the door it takes over from and reason why, both as the JS template inserts them.
func ConductorContinueBrief(to, from, reason string) string {
	flip := Command("flip")
	return "# Continue as conductor (" + to + " door)\n\n" +
		"You are taking over this project from the " + from + " conductor (" + reason + ").\n" +
		"Read .orchestrator/STATE.md and the mailbox, verify recorded facts against the tree, then execute the exact next action and keep following the loop.\n" +
		"At each brief boundary run: " + flip + " decide --active " + to + "\n" +
		"If it says handback or flip: update STATE.md, post a handoff, run " + flip + " run --to <door> --from " + to + ", and stop.\n" +
		"If it says gate: the project is waiting for the human; update STATE.md and stop, without running " + flip + " pause, run or gate.\n" +
		"You cannot ask the human interactively: put each question in the mailbox with the answer you recommend and whether it blocks you (" + Command("mailbox") + " post --from conductor-" + to + " --to human --kind question --subject \"...\" --body \"...\" --recommended \"<a complete reply>\" --blocking true|false) and continue with independent work, or stop at human gates.\n" +
		"HEADLESS RULE: ending your turn ends this session and kills background subagents. Run every subagent in the FOREGROUND and wait; any background agents from a previous headless session are gone, re-run them in the foreground. " +
		"Do not finish until the loop is complete or you have handed off with " + flip + " run/pause, or stopped at a human gate with " + flip + " gate (never pause for a human gate).\n"
}

// ClaudeExecHelp is what `orch claude-exec --help` prints: the header comment of the JS
// bridge it replaced, as orch help (PE-19).
const ClaudeExecHelp = `Delegate a bounded task to Claude Code (print mode) on the Claude subscription, structured result back.

  orch claude-exec [--model claude-opus-5-5|opus|sonnet|haiku|fable] [--effort low|medium|high|xhigh|max]
                   [--permission-mode acceptEdits|auto|plan|dontAsk] [--allowed-tools "Read,Edit,Bash(go *)"]
                   [--max-turns N] [--max-budget-usd X] [--cwd DIR] [--resume SESSION_ID] [--agent NAME]
                   [--append-system-prompt TEXT] [--json-schema FILE] [--timeout-min 60] [--force] [--quiet]
                   [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]

Prints one JSON object on stdout: { ok, session_id, final_message, usage, model_usage, cost_usd,
num_turns, permission_denials, duration_ms, record }. Exit codes: 0 ok, 1 error, 75 budget refused.`

// CodexExecHelp is what `orch codex-exec --help` prints: the header comment of the JS bridge
// it replaced, as orch help (PE-19).
const CodexExecHelp = `Delegate a bounded task to the Codex CLI on the ChatGPT subscription and get a structured result.

  orch codex-exec [--model gpt-6.1-sol] [--effort low|medium|high|xhigh|max|ultra]
                  [--sandbox read-only|workspace-write|danger-full-access] [--cwd DIR]
                  [--resume THREAD_ID] [--schema FILE] [--timeout-min 60] [--force] [--quiet]
                  [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]

Prints one JSON object on stdout: { ok, thread_id, final_message, usage, files_changed, commands,
duration_ms, record } and streams progress lines to stderr. Exit codes: 0 ok, 1 turn failed,
75 budget gate refused, 2 usage error.`
