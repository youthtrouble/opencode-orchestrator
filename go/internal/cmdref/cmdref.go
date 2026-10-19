// Package cmdref holds the orchestrator command lines that are embedded in texts read by
// agents (supervisor reminders, briefs). While the JS runtime is installed alongside orch,
// these must stay byte-identical to the strings the JS writes; the switch to `orch <cmd>`
// happens in one place, here.
package cmdref

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
