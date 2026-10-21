// Every request passes the same gate before any routing (plan D4):
//   config -> Access JWT -> route class -> identity pin -> machine token | browser write checks
//   -> declared body size -> dispatch.
// An unknown path therefore answers 403 to a caller without a valid identity, and 404 only after.
import { createRemoteJWKSet, type JWTVerifyGetKey } from "jose";
import { browserIdentity, machineIdentity, verifyAccessJwt, type Identity } from "./auth/access.ts";
import { verifyMachineToken } from "./auth/machine.ts";
import { MAX_BODY_BYTES, payloadTooLarge } from "./body.ts";
import { readConfig, type Config, type WorkerEnv } from "./env.ts";
import { ForbiddenError, HttpError, toResponse } from "./http.ts";
import { notifierFor } from "./notify/notifier.ts";
import { notifyOnAccepted } from "./notify/triggers.ts";
import { countingDb, QueryCounter } from "./queries.ts";
import { appRoutes, matchRoute, routeClassFor, type HandlerContext, type Route, type RouteClass } from "./routes.ts";

export interface WorkerDeps {
  /** The key-set function that verifies Access JWTs issued by a team domain. */
  keysFor: (teamDomain: string) => JWTVerifyGetKey;
  routes: readonly Route[];
  now: () => Date;
}

export function createWorker(deps: WorkerDeps): ExportedHandler<WorkerEnv> {
  return {
    async fetch(request, env, executionCtx) {
      const path = new URL(request.url).pathname;
      const waitUntil = (task: Promise<unknown>) => executionCtx.waitUntil(task);
      try {
        return await gateThenDispatch(request, env, deps, path, waitUntil);
      } catch (error) {
        return toResponse(error, path);
      }
    },
  };
}

async function gateThenDispatch(
  request: Request,
  env: WorkerEnv,
  deps: WorkerDeps,
  path: string,
  waitUntil: HandlerContext["waitUntil"],
) {
  const cfg = readConfig(env);
  const now = deps.now();
  const payload = await verifyAccessJwt(accessJwtOf(request), {
    issuer: cfg.accessTeamDomain,
    audience: cfg.accessAud,
    keys: deps.keysFor(cfg.accessTeamDomain),
    now,
  });
  const routeClass = routeClassFor(path);
  const identity = routeClass === "machine" ? machineIdentity(payload, cfg) : browserIdentity(payload, cfg);
  if (routeClass === "machine") {
    await verifyMachineToken(request.headers.get("Authorization"), cfg.machineTokenSha256);
  } else if (isWrite(request.method)) {
    checkBrowserWrite(request, cfg);
  }
  checkDeclaredLength(request);
  return dispatch({ request, env, cfg, now, identity, routeClass, routes: deps.routes, path, waitUntil });
}

function accessJwtOf(request: Request): string {
  const token = request.headers.get("Cf-Access-Jwt-Assertion");
  if (!token) throw new ForbiddenError("jwt_missing");
  return token;
}

function isWrite(method: string): boolean {
  return method !== "GET" && method !== "HEAD";
}

/** CSRF defence: a cross-site form cannot send JSON, and a foreign page cannot fake Origin. */
function checkBrowserWrite(request: Request, cfg: Config): void {
  const mediaType = request.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase();
  if (mediaType !== "application/json") throw new ForbiddenError("browser_write_not_json");
  if (request.headers.get("Origin") !== cfg.publicOrigin) throw new ForbiddenError("browser_write_foreign_origin");
}

/** A malformed Content-Length is a client syntax error (400); a well-formed one over the cap is 413. */
function checkDeclaredLength(request: Request): void {
  const declared = request.headers.get("Content-Length");
  if (declared === null) return;
  if (!/^\d+$/.test(declared)) throw new HttpError(400, "bad_request", "Content-Length is not a byte count");
  if (Number(declared) > MAX_BODY_BYTES) throw payloadTooLarge();
}

interface Dispatch {
  request: Request;
  env: WorkerEnv;
  cfg: Config;
  now: Date;
  identity: Identity;
  routeClass: RouteClass;
  routes: readonly Route[];
  path: string;
  waitUntil: HandlerContext["waitUntil"];
}

async function dispatch(d: Dispatch): Promise<Response> {
  const match = matchRoute(d.routes, d.routeClass, d.request.method, d.path);
  if (match) {
    const { request, env, cfg, now, identity, waitUntil } = d;
    const queries = new QueryCounter();
    const db = countingDb(env.DB, queries);
    return match.route.handler({ request, env, cfg, db, queries, now, identity, params: match.params, waitUntil });
  }
  if (d.routeClass === "browser" && !isWrite(d.request.method) && !d.path.startsWith("/api/")) {
    return d.env.ASSETS.fetch(d.request);
  }
  throw new HttpError(404, "not_found", "not found");
}

// One key set per isolate: jose caches the Access keys and refetches on an unknown `kid`, which
// covers Access's key rotation.
const remoteKeySets = new Map<string, JWTVerifyGetKey>();

function remoteAccessKeys(teamDomain: string): JWTVerifyGetKey {
  let keys = remoteKeySets.get(teamDomain);
  if (!keys) {
    keys = createRemoteJWKSet(new URL(`${teamDomain}/cdn-cgi/access/certs`));
    remoteKeySets.set(teamDomain, keys);
  }
  return keys;
}

const routes = appRoutes({ onPushAccepted: notifyOnAccepted(notifierFor) });

export default createWorker({ keysFor: remoteAccessKeys, routes, now: () => new Date() });
