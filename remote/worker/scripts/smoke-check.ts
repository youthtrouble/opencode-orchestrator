// The deploy workflow's smoke check: with no credentials at all, the page must send a browser to Cloudflare
// Access and a machine route must refuse. It uses no secret, so it proves only that Access still stands in
// front of the hostname and that the hostname answers. Access answers both requests itself, so a Worker that
// throws on every request still passes: this catches a deploy that dropped Access or the hostname, not a
// broken Worker.
//
//   node scripts/smoke-check.ts https://trains.example.com
//
// It prints the paths and statuses it saw, never the hostname: logs of a public repository's runs are public.
import { fileURLToPath } from "node:url";

type Fetch = (url: string, init: RequestInit) => Promise<Response>;

const MACHINE_ROUTE = "/api/v1/machine/pull";
// Access answers an unauthenticated request with a redirect to its login page, or 401/403 for a client that
// does not look like a browser; the Worker itself refuses with 401/403.
const REFUSED = new Set([401, 403]);

function redirectsToAccess(response: Response): boolean {
  const location = response.headers.get("location");
  if (response.status !== 302 || !location) return false;
  try {
    const url = new URL(location);
    return url.protocol === "https:" && url.hostname.endsWith(".cloudflareaccess.com");
  } catch {
    return false; // a Location that is not a URL is not Access's login page
  }
}

/** Returns what failed, one line per check; an empty list means the deploy looks healthy. */
export async function smokeCheck(origin: string, fetch: Fetch): Promise<string[]> {
  const failures: string[] = [];
  const get = async (path: string): Promise<Response | undefined> => {
    try {
      return await fetch(`${origin}${path}`, { redirect: "manual" });
    } catch (error) {
      // The error's message may name the hostname, so only its kind is reported.
      const code = (error as { cause?: { code?: string } }).cause?.code ?? (error as Error).name;
      failures.push(`GET ${path} failed (${code})`);
      return undefined;
    }
  };
  const page = await get("/");
  if (page && !redirectsToAccess(page)) failures.push(`GET / answered ${page.status}, not a 302 to Access's login page`);
  const machine = await get(MACHINE_ROUTE);
  if (machine && !REFUSED.has(machine.status) && !redirectsToAccess(machine)) {
    failures.push(`GET ${MACHINE_ROUTE} without credentials answered ${machine.status}, not a refusal`);
  }
  return failures;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const origin = process.argv[2];
  if (!origin?.startsWith("https://")) {
    console.error("usage: node scripts/smoke-check.ts https://<hostname>");
    process.exit(2);
  }
  const failures = await smokeCheck(origin, (url, init) => fetch(url, { ...init, signal: AbortSignal.timeout(15_000) }));
  for (const failure of failures) console.error(`smoke check: ${failure}`);
  if (failures.length > 0) process.exit(1);
  console.log("smoke check: the page redirects to Access and the machine route refuses");
}
