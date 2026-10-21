// The owner's phone, simulated: the page's own API calls, made through the Access emulator with a session
// cookie (the owner's unless told otherwise) and the page's origin, as the browser would make them.
import { randomUUID } from "node:crypto";
import { request } from "node:http";
import { OWNER_COOKIE } from "./access-emulator.ts";

/** The Worker's PUBLIC_ORIGIN in the run: a browser write must come from it. */
export const PAGE_ORIGIN = "https://trains.example.com";
/** Where the page was served before the hostname moved to PAGE_ORIGIN. */
export const OLD_PAGE_ORIGIN = "https://work-trains.example.workers.dev";

// Loose views of the summary: the protocol tests own its exact shape.
export type Json = Record<string, any>;

export interface Reply {
  status: number;
  body: Json;
}

export interface PageLoad {
  status: number;
  location: string | undefined;
}

export interface PhoneOptions {
  cookie?: string;
  origin?: string;
}

export class Phone {
  readonly #accessUrl: string;
  readonly #cookie: string;
  readonly #origin: string;

  constructor(accessUrl: string, { cookie = OWNER_COOKIE, origin = PAGE_ORIGIN }: PhoneOptions = {}) {
    this.#accessUrl = accessUrl;
    this.#cookie = cookie;
    this.#origin = origin;
  }

  /** The summary call as it answered, refusals included. */
  loadSummary(): Promise<Reply> {
    return this.#call("GET", "/api/v1/summary");
  }

  async summary(): Promise<Json> {
    const { status, body } = await this.loadSummary();
    if (status !== 200) throw new Error(`GET /api/v1/summary answered ${status}`);
    return body;
  }

  async project(name: string): Promise<Json> {
    const project = (await this.summary()).projects.find((candidate: Json) => candidate.name === name);
    if (project === undefined) throw new Error(`the summary has no project ${name}`);
    return project;
  }

  async question(projectName: string, subject: string): Promise<Json> {
    const { questions } = await this.project(projectName);
    const question = questions.find((candidate: Json) => candidate.subject === subject);
    if (question === undefined) throw new Error(`${projectName} has no open question "${subject}"`);
    return question;
  }

  accept(questionId: string, clientKey = randomUUID()): Promise<Reply> {
    return this.#call("POST", `/api/v1/questions/${questionId}/answer`, { kind: "accept", client_key: clientKey });
  }

  reply(questionId: string, text: string, clientKey = randomUUID()): Promise<Reply> {
    return this.#call("POST", `/api/v1/questions/${questionId}/answer`, { kind: "typed", text, client_key: clientKey });
  }

  undoAnswer(answerId: string): Promise<Reply> {
    return this.#call("DELETE", `/api/v1/answers/${answerId}`);
  }

  act(projectKey: string, kind: "pause" | "resume", clientKey = randomUUID()): Promise<Reply> {
    return this.#call("POST", `/api/v1/projects/${projectKey}/actions`, { kind, client_key: clientKey });
  }

  /** The page (or an asset) as a browser on `host` loads it; only the status and any redirect matter. */
  openPage(path: string, host: string): Promise<PageLoad> {
    return getOnHost(this.#accessUrl, host, path, { Cookie: this.#cookie });
  }

  async notifications(): Promise<Json[]> {
    const { status, body } = await this.#call("GET", "/__e2e/notifications");
    if (status !== 200) throw new Error(`GET /__e2e/notifications answered ${status}`);
    return body.notifications;
  }

  async #call(method: string, path: string, payload?: Json): Promise<Reply> {
    const headers: Record<string, string> = { Cookie: this.#cookie, Origin: this.#origin };
    // The Worker takes a write, DELETE included, only as JSON (its CSRF defence).
    if (method !== "GET") headers["Content-Type"] = "application/json";
    const response = await fetch(this.#accessUrl + path, {
      method,
      headers,
      body: payload === undefined ? undefined : JSON.stringify(payload),
      redirect: "manual",
    });
    return { status: response.status, body: await response.json() };
  }
}

/**
 * A GET through Access addressed to `host`, as a client of that hostname sends it. fetch cannot set Host,
 * so this is a plain HTTP request; its body is drained and dropped.
 */
export function getOnHost(accessUrl: string, host: string, path: string, headers: Record<string, string>) {
  const { hostname, port } = new URL(accessUrl);
  return new Promise<PageLoad>((resolve, reject) => {
    const outbound = request({ hostname, port, path, headers: { ...headers, Host: host } }, (response) => {
      response.resume();
      response.on("end", () => resolve({ status: response.statusCode ?? 0, location: response.headers.location }));
    });
    outbound.on("error", reject);
    outbound.end();
  });
}
