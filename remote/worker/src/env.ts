import { NotConfiguredError } from "./http.ts";

// Identity values are Worker secrets, never committed config, so a deploy without them must fail
// closed. NOTIFY_* are optional: without both the Worker simply sends no email.
// OWNER_EMAIL is a comma-separated allowlist of the emails the browser routes accept (one is fine).
// PUBLIC_ORIGIN is exactly one origin, the hostname the page is served on: moving to a new hostname is
// a one-step cut-over (set PUBLIC_ORIGIN to it), and a write from any other origin is refused.
const REQUIRED_SECRETS = [
  "ACCESS_TEAM_DOMAIN",
  "ACCESS_AUD",
  "OWNER_EMAIL",
  "ACCESS_SERVICE_CLIENT_ID",
  "MACHINE_TOKEN_SHA256",
  "PUBLIC_ORIGIN",
] as const;

type RequiredSecret = (typeof REQUIRED_SECRETS)[number];
type OptionalSecret = "NOTIFY_FROM" | "NOTIFY_TO";

/**
 * The generated binding types plus what `wrangler types` cannot see: the secrets, and the optional
 * `EMAIL` send_email binding, which only the owner's deploy config declares.
 */
export type WorkerEnv = Env & Partial<Record<RequiredSecret | OptionalSecret, string>> & { EMAIL?: SendEmail };

export interface Config {
  accessTeamDomain: string;
  accessAud: string;
  /** The owners' emails, lower-cased and de-duplicated, so identity checks compare case-insensitively. */
  ownerEmails: ReadonlySet<string>;
  accessServiceClientId: string;
  machineTokenSha256: Uint8Array;
  publicOrigin: string;
  notifyFrom: string | null;
  notifyTo: string | null;
}

const SECRET_FORMATS: Partial<Record<RequiredSecret, (value: string) => boolean>> = {
  ACCESS_TEAM_DOMAIN: isAccessTeamDomain,
  OWNER_EMAIL: (value) => parseEmailList(value) !== null,
  MACHINE_TOKEN_SHA256: (value) => /^[0-9a-f]{64}$/.test(value),
  PUBLIC_ORIGIN: isHttpsOrigin,
};

/** Returns the typed config, or throws NotConfiguredError naming (never echoing) each bad secret. */
export function readConfig(env: WorkerEnv): Config {
  const unusable = [...REQUIRED_SECRETS.filter((name) => !isUsable(name, env[name])), ...unusableNotifySecrets(env)];
  if (unusable.length > 0) throw new NotConfiguredError(unusable);
  const secret = (name: RequiredSecret) => env[name] as string;
  return {
    accessTeamDomain: secret("ACCESS_TEAM_DOMAIN"),
    accessAud: secret("ACCESS_AUD"),
    ownerEmails: new Set(parseEmailList(secret("OWNER_EMAIL"))),
    accessServiceClientId: secret("ACCESS_SERVICE_CLIENT_ID"),
    machineTokenSha256: hexToBytes(secret("MACHINE_TOKEN_SHA256")),
    publicOrigin: secret("PUBLIC_ORIGIN"),
    notifyFrom: env.NOTIFY_FROM || null,
    notifyTo: env.NOTIFY_TO || null,
  };
}

/** An Access team domain as Access writes it in a JWT's `iss`: `https://<team>.cloudflareaccess.com`. */
export function isAccessTeamDomain(value: string): boolean {
  return /^https:\/\/[a-z0-9-]+\.cloudflareaccess\.com$/.test(value);
}

/**
 * OWNER_EMAIL is a comma-separated allowlist, so one deploy can admit more than one sign-in of the
 * same owner; a single address is a list of one. Returns the addresses lower-cased, or null when any
 * entry, an empty one included, is not an email: a typo must fail closed, never shorten the list.
 */
function parseEmailList(value: string): string[] | null {
  const emails = value.split(",").map((entry) => entry.trim().toLowerCase());
  return emails.every(isEmailShaped) ? emails : null;
}

function isUsable(name: RequiredSecret, value: string | undefined): boolean {
  if (!value) return false;
  return SECRET_FORMATS[name]?.(value) ?? true;
}

/**
 * The value is compared byte for byte with the browser's Origin header, so it must be exactly an
 * origin: equality with `url.origin` rules out a path, query, fragment, credentials or trailing slash.
 */
function isHttpsOrigin(value: string): boolean {
  if (!URL.canParse(value)) return false;
  const url = new URL(value);
  return url.protocol === "https:" && url.origin === value;
}

/** Email is off unless both are set; one without the other, or a malformed address, is a misconfig. */
function unusableNotifySecrets(env: WorkerEnv): OptionalSecret[] {
  const from = env.NOTIFY_FROM || null;
  const to = env.NOTIFY_TO || null;
  if (from === null && to === null) return [];
  const bad: OptionalSecret[] = [];
  if (!isEmailShaped(from)) bad.push("NOTIFY_FROM");
  if (!isEmailShaped(to)) bad.push("NOTIFY_TO");
  return bad;
}

/** A plausibility check, not RFC 5322: exactly one `@`, something on each side, no whitespace. */
function isEmailShaped(value: string | null): boolean {
  return value !== null && /^[^\s@]+@[^\s@]+$/.test(value);
}

function hexToBytes(hex: string): Uint8Array {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i++) bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return bytes;
}
