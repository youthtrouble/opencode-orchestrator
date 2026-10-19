// Package cmdref holds the orchestrator command lines that are embedded in texts read by
// agents (supervisor reminders, briefs). While the JS runtime is installed alongside orch,
// these must stay byte-identical to the strings the JS writes; the switch to `orch <cmd>`
// happens in one place, here.
package cmdref

import "strings"

// Script is the command line that runs the named orchestrator script, e.g.
// Script("mailbox") is "node ~/.config/opencode/orchestrator/bin/mailbox.mjs".
func Script(name string) string {
	return "node ~/.config/opencode/orchestrator/bin/" + ScriptFile(name)
}

// ScriptFile is the file name of the named orchestrator script, as the JS scripts name
// themselves (path.basename(process.argv[1])), e.g. ScriptFile("claude-exec") is
// "claude-exec.mjs".
func ScriptFile(name string) string {
	return name + ".mjs"
}

// MailboxReply is the command line `mailbox inbox` tells the human to answer a question with,
// followed by the message id prefix and the answer.
func MailboxReply() string {
	return Script("mailbox") + " reply"
}

// FlipGateClear is the command line that clears a human gate, which `flip gate` tells the
// human to run.
func FlipGateClear() string {
	return Script("flip") + " gate --clear"
}

// WatchFollow is the command line kickoff tells the human to watch the run with.
func WatchFollow() string {
	return Script("watch") + " --follow"
}

// KickoffUsage is the usage line `kickoff.mjs` prints for --help or without a brief.
func KickoffUsage() string {
	return "usage: " + ScriptFile("kickoff") + " --brief FILE [--brief FILE2] [--door auto|claude|opencode] [--headless] [--dry-run]"
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
// conductor-continue.md for the conductor it launches at door to (bin/flip.mjs l.74): from is
// the door it takes over from and reason why, both as the JS template inserts them.
func ConductorContinueBrief(to, from, reason string) string {
	flip := ScriptFile("flip")
	return "# Continue as conductor (" + to + " door)\n\n" +
		"You are taking over this project from the " + from + " conductor (" + reason + ").\n" +
		"Read .orchestrator/STATE.md and the mailbox, verify recorded facts against the tree, then execute the exact next action and keep following the loop.\n" +
		"At each brief boundary run: " + Script("flip") + " decide --active " + to + "\n" +
		"If it says handback or flip: update STATE.md, post a handoff, run " + flip + " run --to <door> --from " + to + ", and stop.\n" +
		"You cannot ask the human interactively: put questions in the mailbox (kind question) and continue with independent work, or stop at human gates.\n" +
		"HEADLESS RULE: ending your turn ends this session and kills background subagents. Run every subagent in the FOREGROUND and wait; any background agents from a previous headless session are gone, re-run them in the foreground. " +
		"Do not finish until the loop is complete or you have handed off with " + flip + " run/pause, or stopped at a human gate with " + flip + " gate (never pause for a human gate).\n"
}

// ClaudeExecHelp is what `claude-exec.mjs --help` prints: lines 2-12 of the script, the
// header comment and the import line that follows it (the JS slices its own source).
const ClaudeExecHelp = `// Delegate a bounded task to Claude Code (print mode) on the Claude subscription, structured result back.
//
//   claude-exec.mjs [--model claude-opus-5-5|opus|sonnet|haiku|fable] [--effort low|medium|high|xhigh|max]
//                   [--permission-mode acceptEdits|auto|plan|dontAsk] [--allowed-tools "Read,Edit,Bash(go *)"]
//                   [--max-turns N] [--max-budget-usd X] [--cwd DIR] [--resume SESSION_ID] [--agent NAME]
//                   [--append-system-prompt TEXT] [--json-schema FILE] [--timeout-min 60] [--force] [--quiet]
//                   [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]
//
// Prints one JSON object on stdout: { ok, session_id, final_message, usage, model_usage, cost_usd,
// num_turns, permission_denials, duration_ms, record }. Exit codes: 0 ok, 1 error, 75 budget refused.
import { spawn } from "node:child_process";`

// CodexExecHelp is what `codex-exec.mjs --help` prints: lines 2-12 of the script, the header
// comment and the import line that follows it.
const CodexExecHelp = `// Delegate a bounded task to the Codex CLI on the ChatGPT subscription and get a structured result.
//
//   codex-exec.mjs [--model gpt-6.1-sol] [--effort low|medium|high|xhigh|max|ultra]
//                  [--sandbox read-only|workspace-write|danger-full-access] [--cwd DIR]
//                  [--resume THREAD_ID] [--schema FILE] [--timeout-min 60] [--force] [--quiet]
//                  [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]
//
// Prints one JSON object on stdout: { ok, thread_id, final_message, usage, files_changed, commands,
// duration_ms, record } and streams progress lines to stderr. Exit codes: 0 ok, 1 turn failed,
// 75 budget gate refused, 2 usage error.
import { spawn } from "node:child_process";`
