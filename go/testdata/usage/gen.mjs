// Regenerates the *.golden files beside it from the JS normalizers in this checkout:
//   node go/testdata/usage/gen.mjs
// Each golden is JSON.stringify(normalized, null, 2) without a trailing newline.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { normalizeClaudeUsage } from "../../../orchestrator/lib/claude-usage.mjs";
import { normalizeCodex } from "../../../orchestrator/lib/usage.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const read = (name) => JSON.parse(fs.readFileSync(path.join(here, name), "utf8"));
const write = (name, value) => fs.writeFileSync(path.join(here, name), JSON.stringify(value, null, 2));

const keychain = { token: "unused", source: "claude-code-keychain", plan: "team", tier: "default_claude_max_5x" };
const credentialsFile = { token: "unused", source: "claude-code-credentials-file", plan: "pro" };
write("claude-oauth-usage.golden", normalizeClaudeUsage(read("claude-oauth-usage.json"), keychain));
write("claude-legacy.golden", normalizeClaudeUsage(read("claude-legacy.json"), credentialsFile));
write("claude-sparse.golden", normalizeClaudeUsage(read("claude-sparse.json"), { token: "unused", source: "opencode-auth" }));

write("codex.golden", normalizeCodex({ account: read("codex-account.json"), limits: read("codex-ratelimits.json") }));
write("codex-by-id.golden", normalizeCodex({ account: null, limits: read("codex-ratelimits-by-id.json") }));
write("codex-empty.golden", normalizeCodex({ account: undefined, limits: undefined }));
