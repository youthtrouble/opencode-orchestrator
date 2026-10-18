// Incident log: append-only, redacted, fingerprinted. Local only (~/.local/state/opencode/orchestrator/incidents.jsonl).
// The audit skill and /retro read it; `incident.mjs share` produces a sanitized bundle you may attach to an issue by hand.
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export const INCIDENTS = path.join(os.homedir(), ".local", "state", "opencode", "orchestrator", "incidents.jsonl");
export const KINDS = ["bug", "limit", "misroute", "permission", "crash", "timeout", "contract", "human-correction", "flip", "note"];

export function redact(text) {
  return String(text ?? "")
    .replace(new RegExp(os.homedir().replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g"), "~")
    .replace(/\/Users\/[^/\s]+/g, "~")
    .replace(/\/home\/[^/\s]+/g, "~")
    .replace(/[\w.+-]+@[\w-]+\.[\w.-]+/g, "<email>")
    .replace(/\b(sk|rt|at|eyJ)[A-Za-z0-9._-]{20,}/g, "<token>")
    .replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, "<uuid>")
    .replace(/\bses_[A-Za-z0-9]+/g, "<session>")
    .slice(0, 2000);
}

export function fingerprint(component, kind, message) {
  const norm = redact(message).toLowerCase().replace(/\d+/g, "N").replace(/\s+/g, " ").slice(0, 200);
  return crypto.createHash("sha256").update(`${component}|${kind}|${norm}`).digest("hex").slice(0, 12);
}

export function version() {
  try { return fs.readFileSync(path.join(path.dirname(new URL(import.meta.url).pathname), "..", "..", ".git", "HEAD"), "utf8").trim().slice(0, 40); } catch { return "unknown"; }
}

export function record({ component, kind = "bug", summary, detail = "", context = {}, project = null }) {
  if (!component || !summary) throw new Error("incident needs component and summary");
  if (!KINDS.includes(kind)) kind = "note";
  const entry = {
    at: new Date().toISOString(),
    fp: fingerprint(component, kind, summary),
    component, kind,
    summary: redact(summary).slice(0, 300),
    detail: redact(detail),
    context: Object.fromEntries(Object.entries(context).map(([k, v]) => [k, typeof v === "string" ? redact(v) : v])),
    project: project ? path.basename(project) : null,
    version: version(),
    platform: process.platform,
  };
  fs.mkdirSync(path.dirname(INCIDENTS), { recursive: true });
  fs.appendFileSync(INCIDENTS, JSON.stringify(entry) + "\n");
  return entry;
}

export function read({ since = null, kind = null, component = null } = {}) {
  if (!fs.existsSync(INCIDENTS)) return [];
  return fs.readFileSync(INCIDENTS, "utf8").split("\n").filter(Boolean).map((l) => { try { return JSON.parse(l); } catch { return null; } }).filter(Boolean)
    .filter((e) => (!since || e.at >= since) && (!kind || e.kind === kind) && (!component || e.component === component));
}

export function summarize(entries) {
  const byFp = new Map();
  for (const e of entries) {
    const s = byFp.get(e.fp) || { fp: e.fp, component: e.component, kind: e.kind, summary: e.summary, count: 0, first: e.at, last: e.at, projects: new Set() };
    s.count++; s.last = e.at; if (e.project) s.projects.add(e.project);
    byFp.set(e.fp, s);
  }
  return [...byFp.values()].map((s) => ({ ...s, projects: [...s.projects] })).sort((a, b) => b.count - a.count || (a.last < b.last ? 1 : -1));
}
