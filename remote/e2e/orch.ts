// The real orch, built from go/ and run as the owner's Mac would run it, in a sandbox: a temp HOME, a remote
// config naming the Access emulator, and a fake security(1) that answers the three Keychain reads from the
// environment, so no secret is ever an argument or a file.
import { execFile, execFileSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, writeFileSync } from "node:fs";
import { delimiter, join } from "node:path";
import { fileURLToPath } from "node:url";

const GO_MODULE = fileURLToPath(new URL("../../go/", import.meta.url));
// One cycle makes at most three requests of at most 15 s each.
const RUN_TIMEOUT_MS = 60_000;

export interface Credentials {
  machineToken: string;
  accessClientId: string;
  accessClientSecret: string;
}

export interface OrchResult {
  code: number;
  stdout: string;
  stderr: string;
}

const FAKE_SECURITY = `#!/bin/sh
# Answers find-generic-password for the Work Trains items from the run's environment, as security(1) would
# from the login Keychain: the value with -w, the item's keychain without; 44 for any other item.
[ "$1" = find-generic-password ] || exit 9
shift
account=
print=
while [ $# -gt 0 ]; do
  case $1 in
    -a) account=$2; shift 2 ;;
    -w) print=1; shift ;;
    *) shift ;;
  esac
done
case $account in
  machine-token) value=$E2E_MACHINE_TOKEN ;;
  access-client-id) value=$E2E_ACCESS_CLIENT_ID ;;
  access-client-secret) value=$E2E_ACCESS_CLIENT_SECRET ;;
  *) echo 'security: The specified item could not be found in the keychain.' >&2; exit 44 ;;
esac
if [ -n "$print" ]; then printf '%s\\n' "$value"; else echo 'keychain: "login.keychain-db"'; fi
`;

/** Builds orch into `dir` and returns its path. GOCACHE and the rest come from the caller's environment. */
export function buildOrch(dir: string): string {
  const binary = join(dir, "orch");
  execFileSync("go", ["build", "-o", binary, "./cmd/orch"], { cwd: GO_MODULE, stdio: "inherit" });
  return binary;
}

export class OrchSandbox {
  readonly home: string;
  /** The one root the remote config scans for projects. */
  readonly projectsRoot: string;
  readonly stateFile: string;
  readonly #binary: string;
  readonly #env: NodeJS.ProcessEnv;
  readonly #transcript: string[] = [];

  /** Lays out the sandbox under `dir`; init() then points it at the Worker. */
  constructor(dir: string, binary: string, credentials: Credentials) {
    this.home = join(dir, "home");
    this.projectsRoot = join(dir, "projects");
    this.stateFile = join(this.home, ".local", "state", "opencode", "orchestrator", "remote", "state.json");
    this.#binary = binary;
    const bin = join(dir, "bin");
    for (const path of [bin, this.projectsRoot, join(this.home, ".config", "opencode", "orchestrator")]) {
      mkdirSync(path, { recursive: true });
    }
    writeFileSync(join(bin, "security"), FAKE_SECURITY);
    chmodSync(join(bin, "security"), 0o755);
    // An empty registry: a pause with no conductor running gates the home door, opencode by default.
    writeFileSync(join(this.home, ".config", "opencode", "orchestrator", "models.json"), "{}\n");
    this.#env = {
      HOME: this.home,
      // The fake security(1) first; git from wherever the runner keeps it, which need not be /usr/bin.
      PATH: [bin, directoryHolding("git"), "/usr/bin", "/bin"].join(delimiter),
      ORCH_REMOTE_CONFIG: join(dir, "config", "remote.json"),
      E2E_MACHINE_TOKEN: credentials.machineToken,
      E2E_ACCESS_CLIENT_ID: credentials.accessClientId,
      E2E_ACCESS_CLIENT_SECRET: credentials.accessClientSecret,
    };
  }

  /** `orch remote init` for the Worker at `url`, scanning projectsRoot. */
  async init(url: string): Promise<void> {
    const init = await this.run(["remote", "init", "--url", url, "--root", this.projectsRoot]);
    if (init.code !== 0) throw new Error(`orch remote init failed: ${init.stderr}`);
  }

  /** A project the config finds: a git checkout with an .orchestrator/ directory. */
  createProject(name: string): string {
    const root = join(this.projectsRoot, name);
    mkdirSync(join(root, ".orchestrator"), { recursive: true });
    execFileSync("git", ["init", "--quiet", root], { env: this.#env });
    return root;
  }

  /**
   * Runs orch without blocking the event loop: the Access emulator answering it lives in this process. An
   * exit status, zero or not, is a result; orch failing to start or outliving RUN_TIMEOUT_MS rejects.
   */
  run(args: string[], cwd = this.home): Promise<OrchResult> {
    return new Promise((resolve, reject) => {
      const options = { cwd, env: this.#env, encoding: "utf8" as const, timeout: RUN_TIMEOUT_MS };
      execFile(this.#binary, args, options, (error, stdout, stderr) => {
        this.#transcript.push(stdout, stderr);
        if (error === null) resolve({ code: 0, stdout, stderr });
        else if (typeof error.code === "number") resolve({ code: error.code, stdout, stderr });
        else reject(error);
      });
    });
  }

  /** Everything orch printed in this sandbox so far. */
  get transcript(): string {
    return this.#transcript.join("\n");
  }
}

/** The first directory on this process's PATH holding `command`. */
function directoryHolding(command: string): string {
  const directory = (process.env.PATH ?? "").split(delimiter).find((dir) => dir && existsSync(join(dir, command)));
  if (directory === undefined) throw new Error(`${command} is not on PATH`);
  return directory;
}
