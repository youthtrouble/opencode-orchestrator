#!/usr/bin/env bash
# One-time setup for the orchestrator. Safe to re-run.
set -euo pipefail
echo "== 1. Connect OpenAI (ChatGPT subscription via Codex / opencode) natively to opencode =="
if opencode auth list 2>/dev/null | grep -qi openai; then echo "already connected"; else
  echo "Running: opencode auth login openai --method chatgpt-token-sharing  (opens a browser)"
  opencode auth login openai --method chatgpt-token-sharing || echo "!! login did not complete; re-run this script"
fi
echo
echo "== 2. Anthropic =="
echo "opencode v2 cannot sign in with a Claude subscription (Anthropic only allows that inside Claude Code)."
echo "Claude models are used through Claude Code itself: make sure 'claude' is logged in:"
claude auth status 2>/dev/null | head -3 || true
echo "(If you ever want Anthropic models native in opencode, add an API key: opencode auth login anthropic)"
echo
echo "== 3. Codex CLI =="
codex login status || echo "run: codex login"
echo
echo "== 4. Reload opencode and run the doctor =="
opencode service restart >/dev/null 2>&1 || true
sleep 2
node "$(dirname "$0")/doctor.mjs" || true
echo
echo "Done. Start with:  opencode   (default agent: conductor)   then try  /usage  and  /plan <idea>"
