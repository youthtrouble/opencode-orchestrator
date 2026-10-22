package cli

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/doctor"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/procs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/version"
)

// Doctor implements `orch doctor`: prints one line per check and exits 1 if any failed. Like
// doctor.mjs it takes no arguments.
func Doctor(_ []string, stdio Stdio) int {
	p, err := paths.Resolve(os.Getenv)
	if err != nil {
		return crash(stdio, err)
	}
	home, err := paths.NodeHome(os.LookupEnv)
	if err != nil {
		return crash(stdio, err)
	}
	cfg := usage.NewConfig(p)
	rows := doctor.Check(doctor.Deps{
		Run:         runDoctorCommand,
		Home:        home,
		OrchHome:    p.OrchHome,
		Routing:     func() (*usage.Routing, error) { return usage.GetRouting(cfg, true) },
		ReadFile:    os.ReadFile,
		Exists:      func(path string) bool { _, err := os.Stat(path); return err == nil },
		OnPath:      func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		LookPath:    exec.LookPath,
		OrchVersion: version.String(),
		Executable:  os.Executable,
		SelfTest:    doctor.SelfTest(p.RegistryFile, os.Executable),
	})
	for _, row := range rows {
		fmt.Fprintln(stdio.Out, row)
	}
	if doctor.Failed(rows) {
		return 1
	}
	return 0
}

// runDoctorCommand is doctor.mjs's sh: spawnSync with a timeout, stdout then stderr. The
// timeout's SIGTERM escalates to SIGKILL after childGrace (PE-10).
func runDoctorCommand(name string, args []string, dir string, timeout time.Duration) doctor.Output {
	r := procs.RunSync(procs.SyncSpec{Name: name, Args: args, Dir: dir, Timeout: timeout, Grace: childGrace})
	return doctor.Output{Code: r.Status, Out: r.Stdout + r.Stderr, TimedOut: r.TimedOut}
}
