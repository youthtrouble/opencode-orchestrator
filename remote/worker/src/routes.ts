// The route table WT8 (machine API) and WT9 (browser API) extend. Matching happens only after the
// auth gate has pinned an identity of the route's class.
import type { Identity } from "./auth/access.ts";
import type { Config, WorkerEnv } from "./env.ts";
import { ackHandler } from "./machine/ack.ts";
import { pullHandler } from "./machine/pull.ts";
import { pushHandler, type OnProjectAccepted } from "./machine/push.ts";

export type RouteClass = "machine" | "browser";

export interface HandlerContext {
  request: Request;
  env: WorkerEnv;
  cfg: Config;
  db: D1Database;
  now: Date;
  identity: Identity;
  params: Record<string, string>;
}

export interface Route {
  method: string;
  /** A path such as `/api/v1/answers/:id`; a `:name` segment captures one path segment. */
  pattern: string;
  class: RouteClass;
  handler: (ctx: HandlerContext) => Promise<Response>;
}

export interface RouteMatch {
  route: Route;
  params: Record<string, string>;
}

/** What the routes need from outside the request: hooks the entry point injects. */
export interface RouteDeps {
  onProjectAccepted: OnProjectAccepted;
}

export function appRoutes({ onProjectAccepted }: RouteDeps): Route[] {
  return [
    { method: "POST", pattern: "/api/v1/machine/push", class: "machine", handler: pushHandler(onProjectAccepted) },
    { method: "GET", pattern: "/api/v1/machine/pull", class: "machine", handler: pullHandler },
    { method: "POST", pattern: "/api/v1/machine/ack", class: "machine", handler: ackHandler },
  ];
}

const MACHINE_PREFIX = "/api/v1/machine/";

/** The class follows from the path alone, so it is known before any route is matched. */
export function routeClassFor(path: string): RouteClass {
  return path.startsWith(MACHINE_PREFIX) ? "machine" : "browser";
}

export function matchRoute(
  table: readonly Route[],
  routeClass: RouteClass,
  method: string,
  path: string,
): RouteMatch | null {
  for (const route of table) {
    if (route.class !== routeClass || route.method !== method) continue;
    const params = matchPattern(route.pattern, path);
    if (params) return { route, params };
  }
  return null;
}

function matchPattern(pattern: string, path: string): Record<string, string> | null {
  const expected = pattern.split("/");
  const actual = path.split("/");
  if (expected.length !== actual.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < expected.length; i++) {
    if (expected[i].startsWith(":") && actual[i] !== "") params[expected[i].slice(1)] = actual[i];
    else if (expected[i] !== actual[i]) return null;
  }
  return params;
}
