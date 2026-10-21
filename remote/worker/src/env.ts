import { NotConfiguredError } from "./http.ts";

// Identity values are Worker secrets, never committed config, so a deploy without them must fail
// closed. NOTIFY_* are optional: without both the Worker simply sends no email.
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
  /** Lower-cased, so identity checks compare case-insensitively. */
  ownerEmail: string;
  accessServiceClientId: string;
  machineTokenSha256: Uint8Array;
  publicOrigin: string;
  notifyFrom: string | null;
  notifyTo: string | null;
}

const SECRET_FORMATS: Partial<Record<RequiredSecret, (value: string) => boolean>> = {
  ACCESS_TEAM_DOMAIN: (value) => /^https:\/\/[a-z0-9-]+\.cloudflareaccess\.com$/.test(value),
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
    ownerEmail: secret("OWNER_EMAIL").toLowerCase(),
    accessServiceClientId: secret("ACCESS_SERVICE_CLIENT_ID"),
    machineTokenSha256: hexToBytes(secret("MACHINE_TOKEN_SHA256")),
    publicOrigin: secret("PUBLIC_ORIGIN"),
    notifyFrom: env.NOTIFY_FROM || null,
    notifyTo: env.NOTIFY_TO || null,
  };
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
