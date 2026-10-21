// Test-only Worker entry for remote/e2e (wrangler.e2e.jsonc). It serves the same app as src/index.ts with
// two seams: Access JWTs are checked against the end-to-end run's own public key (E2E_JWKS) instead of a
// team domain's, and notifications are recorded, readable by the owner at GET /__e2e/notifications.
// Nothing here is reachable from a production deploy, whose entry is src/index.ts.
import { createLocalJWKSet, type JSONWebKeySet, type JWTVerifyGetKey } from "jose";
import type { WorkerEnv } from "../src/env.ts";
import { jsonResponse } from "../src/http.ts";
import { createApp } from "../src/index.ts";
import type { Notification, Notifier } from "../src/notify/notifier.ts";
import type { Route } from "../src/routes.ts";

type E2EEnv = WorkerEnv & { E2E_JWKS?: string };

/** Keeps every notification the app sends, for as long as the isolate lives. */
class RecordingNotifier implements Notifier {
  readonly sent: Notification[] = [];

  async send(notification: Notification): Promise<void> {
    this.sent.push(notification);
  }
}

const recorder = new RecordingNotifier();

// A browser route, so the gate admits only the owner's Access JWT to it.
const notificationsRoute: Route = {
  method: "GET",
  pattern: "/__e2e/notifications",
  class: "browser",
  handler: async () => jsonResponse(200, { notifications: recorder.sent }),
};

function localKeys(env: E2EEnv): JWTVerifyGetKey {
  if (!env.E2E_JWKS) throw new Error("E2E_JWKS is not set");
  return createLocalJWKSet(JSON.parse(env.E2E_JWKS) as JSONWebKeySet);
}

// The keys arrive with the first request's env, so the app is built then, once per isolate.
let app: ExportedHandler<WorkerEnv> | null = null;

export default {
  fetch(request, env, executionCtx) {
    if (app === null) {
      const keys = localKeys(env);
      app = createApp({ keysFor: () => keys, notifierFor: () => recorder, extraRoutes: [notificationsRoute] });
    }
    return app.fetch!(request, env, executionCtx);
  },
} satisfies ExportedHandler<E2EEnv>;
