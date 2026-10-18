---
name: orchestrator-audit
description: Audit and refresh the opencode orchestrator from the internet. Checks current model lineups (Fable, Opus, Sonnet, Haiku, GPT-6 Sol/Astra/Luna and successors), prices, subscription limits, opencode/Codex/Claude Code CLI changes, then updates models.json, agents, and docs with evidence, and runs the self-tests. Use monthly or when a model is retired/launched.
---
# Orchestrator audit

Goal: keep `~/.config/opencode/orchestrator` correct against reality. Every change must cite a source
and a date. Never guess a model id; verify it exists in the live catalog.

## 1. Snapshot the current state
```
cd ~/.config/opencode && git status --short && git log --oneline -5
node orchestrator/bin/usage.mjs --refresh
node orchestrator/bin/doctor.mjs
cat orchestrator/models.json
```

## 2. Verify live catalogs (ground truth beats blogs)
- Codex catalog on this account: `codex debug models | python3 -c "import sys,json;[print(m['slug'],m.get('visibility'),m.get('description')) for m in json.load(sys.stdin)['models']]"`
- Anthropic ids reachable from Claude Code: `claude -p --model <id> --max-turns 1 "reply OK"` for each id in models.json (costs ~one tiny call each; skip if the Claude session window is red).
- opencode native models: `opencode models` (requires `opencode auth login openai --method chatgpt-token-sharing`).
- models.dev: `curl -s https://models.dev/api.json | python3 -c "import sys,json;d=json.load(sys.stdin);[print(p, sorted(d[p]['models'])) for p in ('anthropic','openai')]"`

## 3. Research what changed (use researcher role or websearch; cite URLs + dates)
- Anthropic: https://platform.claude.com/docs/en/models/overview , https://code.claude.com/docs/en/model-config , https://support.claude.com/en/articles/15424964 (Fable in plans), https://code.claude.com/docs/en/cli-reference
- OpenAI/Codex: https://learn.chatgpt.com/docs/models , https://learn.chatgpt.com/docs/pricing.md , https://learn.chatgpt.com/docs/non-interactive-mode , https://learn.chatgpt.com/docs/app-server , `codex --version`, `codex exec --help`
- opencode: https://opencode.ai/v2/docs/ (config, agents, plugins, skills, commands, providers), `opencode --version`, https://opencode.ai/config.json , npm `@opencode/plugin` version
- Community signal on strengths (balanced, dated): Artificial Analysis, METR, SWE-bench Pro / Terminal-Bench posts, r/ClaudeAI, r/codex, HN. Record only claims with a source.

## 4. Decide and apply
For each role in models.json ask: is there a newer tier at the same or lower cost? did a default effort change? did a context window change? did a CLI flag the bridges use change (`claude -p`, `codex exec`)? did the usage endpoints change shape (`lib/claude-usage.mjs`, `lib/codex-appserver.mjs`)?
- Edit `orchestrator/models.json` (bump `last_verified`, keep notes with the source).
- Edit `agents/*.md` and `opencode.jsonc` static models to match.
- Edit `AGENTS.md` role table if roles changed.
- If a bridge flag changed, fix `bin/claude-exec.mjs` / `bin/codex-exec.mjs` and re-run the smoke tests.

## 5. Verify
```
cd ~/.config/opencode/orchestrator && node --test tests/*.test.mjs
node bin/usage.mjs --refresh
node bin/codex-exec.mjs --model gpt-6-luna --effort low --sandbox read-only --prompt "Reply OK"
node bin/claude-exec.mjs --model claude-haiku-5-5 --max-turns 1 --permission-mode plan --prompt "Reply OK"
opencode service restart && opencode debug agents | head -c 400 && opencode plugin list | grep orchestrator
```

## 6. Record
Append a dated entry to `orchestrator/CHANGELOG.md` (what changed, why, sources), commit in
`~/.config/opencode` (`git add -A && git commit -m "audit: <date>"`), and summarize for the user:
changes made, things verified, things that could not be verified.
