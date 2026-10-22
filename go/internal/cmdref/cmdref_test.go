package cmdref

import "testing"

func TestCommandIsOrchAndTheSubcommand(t *testing.T) {
	if got := Command("mailbox"); got != "orch mailbox" {
		t.Errorf("Command(mailbox) = %q, want %q", got, "orch mailbox")
	}
}

// mailbox inbox and watch print this command before each question's id prefix.
func TestMailboxReplyNamesOrch(t *testing.T) {
	const want = "orch mailbox reply"
	if got := MailboxReply(); got != want {
		t.Errorf("MailboxReply() = %q, want %q", got, want)
	}
}

// flip gate names this exact command in the human's message body.
func TestFlipGateClearIsTheGateMessageCommand(t *testing.T) {
	const want = "orch flip gate --clear"
	if got := FlipGateClear(); got != want {
		t.Errorf("FlipGateClear() = %q, want %q", got, want)
	}
}

// flip run writes this brief; TestJSReference (go/parity) renders it from the JS source
// through PE-19.
func TestConductorContinueBriefIsTheFlipBrief(t *testing.T) {
	const want = "# Continue as conductor (claude door)\n\n" +
		"You are taking over this project from the opencode conductor (handback: home door).\n" +
		"Read .orchestrator/STATE.md and the mailbox, verify recorded facts against the tree, then execute the exact next action and keep following the loop.\n" +
		"At each brief boundary run: orch flip decide --active claude\n" +
		"If it says handback or flip: update STATE.md, post a handoff, run orch flip run --to <door> --from claude, and stop.\n" +
		"If it says gate: the project is waiting for the human; update STATE.md and stop, without running orch flip pause, run or gate.\n" +
		"You cannot ask the human interactively: put each question in the mailbox with the answer you recommend and whether it blocks you (orch mailbox post --from conductor-claude --to human --kind question --subject \"...\" --body \"...\" --recommended \"<a complete reply>\" --blocking true|false) and continue with independent work, or stop at human gates.\n" +
		"HEADLESS RULE: ending your turn ends this session and kills background subagents. Run every subagent in the FOREGROUND and wait; any background agents from a previous headless session are gone, re-run them in the foreground. " +
		"Do not finish until the loop is complete or you have handed off with orch flip run/pause, or stopped at a human gate with orch flip gate (never pause for a human gate).\n"
	if got := ConductorContinueBrief("claude", "opencode", "handback: home door"); got != want {
		t.Errorf("ConductorContinueBrief differs\n got: %q\nwant: %q", got, want)
	}
}

// orch claude-exec --help prints the JS bridge's header as orch help; TestJSReference
// (go/parity) derives it from the JS source through PE-19.
func TestClaudeExecHelpIsTheScriptHeaderAsOrchHelp(t *testing.T) {
	const want = `Delegate a bounded task to Claude Code (print mode) on the Claude subscription, structured result back.

  orch claude-exec [--model claude-opus-5-5|opus|sonnet|haiku|fable] [--effort low|medium|high|xhigh|max]
                   [--permission-mode acceptEdits|auto|plan|dontAsk] [--allowed-tools "Read,Edit,Bash(go *)"]
                   [--max-turns N] [--max-budget-usd X] [--cwd DIR] [--resume SESSION_ID] [--agent NAME]
                   [--append-system-prompt TEXT] [--json-schema FILE] [--timeout-min 60] [--force] [--quiet]
                   [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]

Prints one JSON object on stdout: { ok, session_id, final_message, usage, model_usage, cost_usd,
num_turns, permission_denials, duration_ms, record }. Exit codes: 0 ok, 1 error, 75 budget refused.`
	if ClaudeExecHelp != want {
		t.Errorf("ClaudeExecHelp differs\n got: %q\nwant: %q", ClaudeExecHelp, want)
	}
}

// orch codex-exec --help prints the JS bridge's header as orch help; TestJSReference
// (go/parity) derives it from the JS source through PE-19.
func TestCodexExecHelpIsTheScriptHeaderAsOrchHelp(t *testing.T) {
	const want = `Delegate a bounded task to the Codex CLI on the ChatGPT subscription and get a structured result.

  orch codex-exec [--model gpt-6.1-sol] [--effort low|medium|high|xhigh|max|ultra]
                  [--sandbox read-only|workspace-write|danger-full-access] [--cwd DIR]
                  [--resume THREAD_ID] [--schema FILE] [--timeout-min 60] [--force] [--quiet]
                  [--prompt TEXT | --prompt-file FILE | TEXT... | stdin]

Prints one JSON object on stdout: { ok, thread_id, final_message, usage, files_changed, commands,
duration_ms, record } and streams progress lines to stderr. Exit codes: 0 ok, 1 turn failed,
75 budget gate refused, 2 usage error.`
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

// kickoff's usage line; TestJSReference (go/parity) reads it from the JS source through PE-19.
func TestKickoffUsageIsTheKickoffUsageLine(t *testing.T) {
	const want = "usage: orch kickoff --brief FILE [--brief FILE2] [--door auto|claude|opencode] [--headless] [--unattended] [--dry-run]"
	if got := KickoffUsage(); got != want {
		t.Errorf("KickoffUsage() = %q, want %q", got, want)
	}
}

// kickoff's watch hint; TestJSReference (go/parity) finds it twice in the JS source through
// PE-19.
func TestWatchFollowIsTheKickoffHint(t *testing.T) {
	const want = "orch watch --follow"
	if got := WatchFollow(); got != want {
		t.Errorf("WatchFollow() = %q, want %q", got, want)
	}
}
