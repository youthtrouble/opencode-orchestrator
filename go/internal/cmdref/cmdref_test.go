package cmdref

import (
	"os"
	"path/filepath"
	"testing"
)

// The JS supervisor writes this exact command into its reminders.
func TestScriptIsTheInstalledNodeCommand(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/mailbox.mjs"
	if got := Script("mailbox"); got != want {
		t.Errorf("Script(mailbox) = %q, want %q", got, want)
	}
}

// Without orch on PATH, bin/mailbox.mjs inbox's exact command precedes each question's id prefix.
func TestMailboxReplyWithoutOrchIsTheJSInboxHint(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/mailbox.mjs reply"
	if got := MailboxReply(false); got != want {
		t.Errorf("MailboxReply(false) = %q, want %q", got, want)
	}
}

// PE-26: with orch on PATH the hint names orch itself.
func TestMailboxReplyWithOrchNamesOrch(t *testing.T) {
	const want = "orch mailbox reply"
	if got := MailboxReply(true); got != want {
		t.Errorf("MailboxReply(true) = %q, want %q", got, want)
	}
}

func TestOrchOnPathFollowsPATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if OrchOnPath() {
		t.Fatal("OrchOnPath() = true with an empty PATH directory")
	}
	if err := os.WriteFile(filepath.Join(dir, "orch"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !OrchOnPath() {
		t.Error("OrchOnPath() = false with an executable orch on PATH")
	}
}

// bin/flip.mjs gate names this exact command in the human's message body.
func TestFlipGateClearIsTheGateMessageCommand(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/flip.mjs gate --clear"
	if got := FlipGateClear(); got != want {
		t.Errorf("FlipGateClear() = %q, want %q", got, want)
	}
}

// flip.mjs run writes this brief; TestJSReference (go/parity) renders it from the JS source.
func TestConductorContinueBriefIsTheFlipBrief(t *testing.T) {
	const want = "# Continue as conductor (claude door)\n\n" +
		"You are taking over this project from the opencode conductor (handback: home door).\n" +
		"Read .orchestrator/STATE.md and the mailbox, verify recorded facts against the tree, then execute the exact next action and keep following the loop.\n" +
		"At each brief boundary run: node ~/.config/opencode/orchestrator/bin/flip.mjs decide --active claude\n" +
		"If it says handback or flip: update STATE.md, post a handoff, run flip.mjs run --to <door> --from claude, and stop.\n" +
		"If it says gate: the project is waiting for the human; update STATE.md and stop, without running flip.mjs pause, run or gate.\n" +
		"You cannot ask the human interactively: put each question in the mailbox with the answer you recommend and whether it blocks you (mailbox.mjs post --from conductor-claude --to human --kind question --subject \"...\" --body \"...\" --recommended \"<a complete reply>\" --blocking true|false) and continue with independent work, or stop at human gates.\n" +
		"HEADLESS RULE: ending your turn ends this session and kills background subagents. Run every subagent in the FOREGROUND and wait; any background agents from a previous headless session are gone, re-run them in the foreground. " +
		"Do not finish until the loop is complete or you have handed off with flip.mjs run/pause, or stopped at a human gate with flip.mjs gate (never pause for a human gate).\n"
	if got := ConductorContinueBrief("claude", "opencode", "handback: home door"); got != want {
		t.Errorf("ConductorContinueBrief differs\n got: %q\nwant: %q", got, want)
	}
}

// bin/claude-exec.mjs records its incidents under its own file name.
func TestScriptFileIsTheScriptBasename(t *testing.T) {
	if got := ScriptFile("claude-exec"); got != "claude-exec.mjs" {
		t.Errorf("ScriptFile(claude-exec) = %q", got)
	}
}

// claude-exec.mjs --help prints lines 2-12 of its own source; TestJSReference (go/parity)
// compares them with the JS source.
func TestClaudeExecHelpIsTheScriptHeader(t *testing.T) {
	const want = `// Delegate a bounded task to Claude Code (print mode) on the Claude subscription, structured result back.
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
	if ClaudeExecHelp != want {
		t.Errorf("ClaudeExecHelp differs\n got: %q\nwant: %q", ClaudeExecHelp, want)
	}
}

// codex-exec.mjs --help prints lines 2-12 of its own source; TestJSReference (go/parity)
// compares them with the JS source.
func TestCodexExecHelpIsTheScriptHeader(t *testing.T) {
	const want = `// Delegate a bounded task to the Codex CLI on the ChatGPT subscription and get a structured result.
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
	if CodexExecHelp != want {
		t.Errorf("CodexExecHelp differs\n got: %q\nwant: %q", CodexExecHelp, want)
	}
}

// kickoff.mjs's opening prompt; TestJSReference (go/parity) renders it from the JS source.
func TestKickoffPromptIsTheKickoffPrompt(t *testing.T) {
	const head = "Ship a.md. Plan first with the architect (size per the brief), then one slice at a time; flip or hand back per the contract at each brief boundary."
	const tail = " Record orchestrator defects as incidents."
	cases := []struct {
		side []string
		want string
	}{
		{nil, head + tail},
		{[]string{"b.md"}, head + " When budget allows, also run b.md as side brief(s) with the cheapest adequate role." + tail},
		{[]string{"b.md", "c.md"}, head + " When budget allows, also run b.md, c.md as side brief(s) with the cheapest adequate role." + tail},
	}
	for _, c := range cases {
		if got := KickoffPrompt("a.md", c.side); got != c.want {
			t.Errorf("KickoffPrompt(a.md, %q)\n got: %q\nwant: %q", c.side, got, c.want)
		}
	}
}

// kickoff.mjs's usage line; TestJSReference (go/parity) reads it from the JS source.
func TestKickoffUsageIsTheKickoffUsageLine(t *testing.T) {
	const want = "usage: kickoff.mjs --brief FILE [--brief FILE2] [--door auto|claude|opencode] [--headless] [--unattended] [--dry-run]"
	if got := KickoffUsage(); got != want {
		t.Errorf("KickoffUsage() = %q, want %q", got, want)
	}
}

// kickoff.mjs's watch hint; TestJSReference (go/parity) finds it twice in the JS source.
func TestWatchFollowIsTheKickoffHint(t *testing.T) {
	const want = "node ~/.config/opencode/orchestrator/bin/watch.mjs --follow"
	if got := WatchFollow(); got != want {
		t.Errorf("WatchFollow() = %q, want %q", got, want)
	}
}
