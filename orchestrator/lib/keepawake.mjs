// Keep the machine from idle-sleeping while a detached worker/supervisor runs. macOS: caffeinate; Linux: systemd-inhibit.
// The assertion lives only as long as the wrapped process; nothing changes system settings.
import { spawnSync } from "node:child_process";

export function keepAwakeWrap(cmd, args, { enabled = true } = {}) {
  if (!enabled) return { cmd, args };
  if (process.platform === "darwin" && spawnSync("which", ["caffeinate"], { stdio: "ignore" }).status === 0) return { cmd: "caffeinate", args: ["-i", "-s", cmd, ...args] };
  if (process.platform === "linux" && spawnSync("which", ["systemd-inhibit"], { stdio: "ignore" }).status === 0) return { cmd: "systemd-inhibit", args: ["--what=idle:sleep", "--why=opencode-orchestrator", cmd, ...args] };
  return { cmd, args };
}
