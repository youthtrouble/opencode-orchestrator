// Renders the deploy config (wrangler.deploy.jsonc) from wrangler.deploy.example.jsonc for one instance, the
// transform the owner otherwise does by hand: the D1 id, the custom-domain hostname and workers.dev on or off.
// The deploy workflow runs it with values from its GitHub environment:
//
//   WORK_TRAINS_D1_ID=... WORK_TRAINS_HOSTNAME=... [WORK_TRAINS_WORKERS_DEV=true] \
//     node scripts/render-deploy-config.ts wrangler.deploy.jsonc
//
// Logs of a public repository's runs are public, so no message here ever contains a value.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const TEMPLATE_PATH = fileURLToPath(new URL("../wrangler.deploy.example.jsonc", import.meta.url));

export interface Instance {
  d1Id: string;
  hostname: string;
  workersDev: boolean;
}

/** A missing or malformed value, or a template that no longer has a placeholder this script fills. */
export class DeployConfigError extends Error {
  override name = "DeployConfigError";
}

const PLACEHOLDER_D1_ID = "00000000-0000-0000-0000-000000000000";
const PLACEHOLDER_HOSTNAME = "trains.example.com";
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
// A bare lower-case DNS name with at least two labels: no scheme, port, path or trailing dot.
const HOSTNAME = /^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** Reads the instance from the environment the workflow sets; WORK_TRAINS_WORKERS_DEV is optional. */
export function instanceFromEnv(env: Record<string, string | undefined>): Instance {
  const d1Id = env.WORK_TRAINS_D1_ID?.trim().toLowerCase() ?? "";
  const hostname = env.WORK_TRAINS_HOSTNAME?.trim().toLowerCase() ?? "";
  const workersDev = env.WORK_TRAINS_WORKERS_DEV?.trim() ?? "";
  if (!UUID.test(d1Id) || d1Id === PLACEHOLDER_D1_ID) {
    throw new DeployConfigError("WORK_TRAINS_D1_ID must be the D1 database's id (a UUID, not the placeholder)");
  }
  if (!HOSTNAME.test(hostname) || hostname === PLACEHOLDER_HOSTNAME) {
    throw new DeployConfigError("WORK_TRAINS_HOSTNAME must be a bare hostname (no scheme, port or path), not the placeholder");
  }
  if (!["", "true", "false"].includes(workersDev)) {
    throw new DeployConfigError("WORK_TRAINS_WORKERS_DEV must be true, false or unset (false)");
  }
  return { d1Id, hostname, workersDev: workersDev === "true" };
}

/** Fills the template's three placeholders, keeping everything else (comments included) byte for byte. */
export function renderDeployConfig(template: string, instance: Instance): string {
  let config = template;
  const fill = (placeholder: string, value: string) => {
    const occurrences = config.split(placeholder).length - 1;
    if (occurrences !== 1) {
      throw new DeployConfigError(`the template must contain ${placeholder} exactly once (found ${occurrences})`);
    }
    config = config.replace(placeholder, () => value);
  };
  fill(`"database_id": "${PLACEHOLDER_D1_ID}"`, `"database_id": "${instance.d1Id}"`);
  fill(`"pattern": "${PLACEHOLDER_HOSTNAME}"`, `"pattern": "${instance.hostname}"`);
  fill(`"workers_dev": false`, `"workers_dev": ${instance.workersDev}`);
  return config;
}

/** Writes the rendered config to `outPath`, owner-only, and never over an existing file. */
export function writeDeployConfig(outPath: string, env: Record<string, string | undefined>): void {
  if (existsSync(outPath)) throw new DeployConfigError(`${outPath} already exists; remove it first`);
  const config = renderDeployConfig(readFileSync(TEMPLATE_PATH, "utf8"), instanceFromEnv(env));
  writeFileSync(outPath, config, { mode: 0o600, flag: "wx" });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const outPath = process.argv[2];
  if (!outPath) {
    console.error("usage: node scripts/render-deploy-config.ts <out.jsonc>");
    process.exit(2);
  }
  try {
    writeDeployConfig(outPath, process.env);
    console.log(`rendered ${outPath}`);
  } catch (error) {
    if (!(error instanceof DeployConfigError)) throw error;
    console.error(`render-deploy-config: ${error.message}`);
    process.exit(1);
  }
}
