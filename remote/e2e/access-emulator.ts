// A local stand-in for Cloudflare Access in front of `wrangler dev`. Like Access it admits a request only
// with the run's service token (the Mac) or the owner's session cookie (the phone), and forwards it with a
// Cf-Access-Jwt-Assertion it signs itself; anything else gets the redirect to the login page Access sends.
// It also injects the faults the end-to-end run needs: a lost ack response, and Access going away.
import { createRequire } from "node:module";
import { createServer, request as forward, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { Socket } from "node:net";
import { pathToFileURL } from "node:url";

// jose is the Worker's dependency, resolved from its node_modules: remote/e2e declares no dependencies.
const workerRequire = createRequire(new URL("../worker/package.json", import.meta.url));
const { exportJWK, generateKeyPair, SignJWT } = await import(pathToFileURL(workerRequire.resolve("jose")).href);

// The placeholder team the repository's identifier guard (worker/test/guard.test.ts) allows.
export const TEAM_DOMAIN = "https://example.cloudflareaccess.com";
export const AUD = "e2e-audience-tag";
export const OWNER_EMAIL = "owner@example.com";
/** The session cookie the phone presents; Access would have set it at login. */
export const OWNER_COOKIE = "CF_Authorization=e2e-owner";
const LOGIN_URL = `${TEAM_DOMAIN}/cdn-cgi/access/login/trains.example.com`;
const KEY_ID = "e2e-key";
const ACK_PATH = "/api/v1/machine/ack";
const OWNER_CLAIMS = { type: "app", email: OWNER_EMAIL, sub: "e2e-owner" };

export interface ServiceToken {
  clientId: string;
  clientSecret: string;
}

interface EmulatorOptions {
  /** The `wrangler dev` origin, such as http://127.0.0.1:8787. */
  upstream: string;
  serviceToken: ServiceToken;
}

/** What a caller may send Access: a forged assertion, or the service token Access consumes. None reaches the Worker. */
const ACCESS_HEADERS = ["cf-access-jwt-assertion", "cf-access-client-id", "cf-access-client-secret"];

export class AccessEmulator {
  readonly #upstream: URL;
  readonly #serviceToken: ServiceToken;
  readonly #privateKey: CryptoKey;
  /** The public key set the Worker verifies with (its E2E_JWKS). */
  readonly jwks: { keys: object[] };
  readonly #sockets = new Set<Socket>();
  #server: Server | null = null;
  #port = 0;
  #dropNextAck = false;

  constructor(options: EmulatorOptions, privateKey: CryptoKey, publicJwk: object) {
    this.#upstream = new URL(options.upstream);
    this.#serviceToken = options.serviceToken;
    this.#privateKey = privateKey;
    this.jwks = { keys: [publicJwk] };
  }

  /** Generates the run's signing key; start() then listens. */
  static async create(options: EmulatorOptions): Promise<AccessEmulator> {
    const { privateKey, publicKey } = await generateKeyPair("RS256", { extractable: true });
    const publicJwk = { ...(await exportJWK(publicKey)), kid: KEY_ID, alg: "RS256", use: "sig" };
    return new AccessEmulator(options, privateKey, publicJwk);
  }

  get url(): string {
    return `http://127.0.0.1:${this.#port}`;
  }

  /** Listens on loopback: a free port the first time, the same port after a stop(). */
  async start(): Promise<void> {
    const server = createServer((request, response) => {
      // A failure to reach the Worker cuts the Mac's connection, as a broken Access edge would.
      this.#handle(request, response).catch((error: unknown) => {
        response.destroy(error instanceof Error ? error : new Error(String(error)));
      });
    });
    server.on("connection", (socket) => {
      this.#sockets.add(socket);
      socket.on("close", () => this.#sockets.delete(socket));
    });
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(this.#port, "127.0.0.1", resolve);
    });
    this.#port = (server.address() as { port: number }).port;
    this.#server = server;
  }

  /** Stops listening and cuts every open connection, as an Access outage would look from the Mac. */
  async stop(): Promise<void> {
    const server = this.#server;
    if (server === null) return;
    this.#server = null;
    const closed = new Promise<void>((resolve) => server.close(() => resolve()));
    for (const socket of this.#sockets) socket.destroy();
    await closed;
  }

  /** A valid assertion for the owner, as a caller who captured one would replay it to Access. */
  ownerAssertion(): Promise<string> {
    return this.#sign(OWNER_CLAIMS);
  }

  /** The next ack reaches the Worker, which settles it, but the Mac's connection closes before any reply. */
  dropNextAckResponse(): void {
    this.#dropNextAck = true;
  }

  async #handle(request: IncomingMessage, response: ServerResponse): Promise<void> {
    const claims = this.#claimsFor(request);
    if (claims === null) {
      response.writeHead(302, { Location: LOGIN_URL }).end();
      return;
    }
    const dropResponse = this.#dropNextAck && request.method === "POST" && request.url === ACK_PATH;
    if (dropResponse) this.#dropNextAck = false;
    const upstream = await this.#forward(request, await this.#sign(claims));
    if (dropResponse) {
      // Let the Worker finish the ack before the Mac loses its connection.
      for await (const _chunk of upstream);
      request.socket.destroy();
      return;
    }
    response.writeHead(upstream.statusCode ?? 502, upstream.headers);
    upstream.pipe(response);
  }

  /** The identity Access would vouch for: the service token, the owner's cookie, or nobody. */
  #claimsFor(request: IncomingMessage): Record<string, string> | null {
    const { clientId, clientSecret } = this.#serviceToken;
    const { "cf-access-client-id": id, "cf-access-client-secret": secret } = request.headers;
    if (id === clientId && secret === clientSecret) {
      return { type: "app", common_name: clientId, sub: "" };
    }
    const cookies = (request.headers.cookie ?? "").split(";").map((cookie) => cookie.trim());
    if (cookies.includes(OWNER_COOKIE)) return OWNER_CLAIMS;
    return null;
  }

  #sign(claims: Record<string, string>): Promise<string> {
    return new SignJWT(claims)
      .setProtectedHeader({ alg: "RS256", kid: KEY_ID })
      .setIssuer(TEAM_DOMAIN)
      .setAudience(AUD)
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(this.#privateKey);
  }

  #forward(request: IncomingMessage, jwt: string): Promise<IncomingMessage> {
    const headers = { ...request.headers, host: this.#upstream.host };
    for (const name of ACCESS_HEADERS) delete headers[name];
    headers["cf-access-jwt-assertion"] = jwt;
    return new Promise((resolve, reject) => {
      const { hostname: host, port } = this.#upstream;
      const outbound = forward({ host, port, method: request.method, path: request.url, headers }, resolve);
      outbound.on("error", reject);
      request.pipe(outbound);
    });
  }
}
