package cli

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/apply"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/client"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/config"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/loop"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/version"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

// The sync loop's commands: once, serve and launchd.

// newRemoteLoop is the sync loop on the real clock, Keychain, apply layer and usage cache.
// Its log lines go to log.
func newRemoteLoop(env remoteEnv, cfg config.Config, log io.Writer) *loop.Loop {
	return &loop.Loop{
		Config:      cfg,
		StateFile:   env.stateFile,
		Home:        env.home,
		OrchVersion: version.Number(),
		Connect: func(machineID string) (loop.Remote, error) {
			credentials, err := client.ReadCredentials(keychain.Read)
			if err != nil {
				return nil, err
			}
			return client.New(cfg.URL, credentials, machineID, version.Number()), nil
		},
		NewApplier: func(projects apply.ProjectResolver) loop.Applier {
			return apply.New(projects, env.paths.RegistryFile, state.Dir(env.home))
		},
		Budget: func() (*wire.Budget, error) { return snapshot.Budget(usage.NewConfig(env.paths)) },
		Alive:  worker.Alive,
		Now:    time.Now,
		Sleep:  loop.SleepContext,
		Jitter: loop.EqualJitter,
		Log:    log,
	}
}

// stopSignals are the signals that end once and serve cleanly: the HTTP call in flight is
// cancelled, the state saved, and the exit is 0 for serve.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// remoteOnceSummary is what `once --json` prints.
type remoteOnceSummary struct {
	loop.Report
	Error *string `json:"error"`
}

// remoteOnce runs one cycle and prints what it did; it exits 1 when the cycle failed. Log lines
// go to stderr, so stdout holds only the summary. It takes serve's lock for the cycle: a once
// overlapping a serve cycle could pull and apply the same answer twice, and apply's
// already-posted check is not atomic across processes.
func remoteOnce(args []string, env remoteEnv, stdio Stdio) error {
	flags := flag.NewFlagSet("once", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	if err := parseRemoteFlags(flags, args); err != nil {
		return err
	}
	cfg, _, err := loadRemoteSetup(env)
	if err != nil {
		return err
	}
	lock, err := loop.AcquireLock(serveLockFile(env), os.Getpid())
	var held *loop.LockHeldError
	if errors.As(err, &held) {
		return serveIsRunning(held.Pid)
	} else if err != nil {
		return err
	}
	defer lock.Release()
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals...)
	defer stop()
	remoteLoop := newRemoteLoop(env, cfg, stdio.Err)
	report, cycleErr := remoteLoop.Cycle(ctx)
	if cycleErr != nil {
		// A local failure names paths under HOME: print it as last_error records it.
		cycleErr = errors.New(remoteLoop.Redact(cycleErr.Error()))
	}
	if !*asJSON {
		fmt.Fprintf(stdio.Out, "pulled %d, applied %d, pushed %d, deferred %d\n", report.Pulled, report.Applied, report.Pushed, report.Deferred)
		return cycleErr
	}
	summary := remoteOnceSummary{Report: report}
	if cycleErr != nil {
		message := cycleErr.Error()
		summary.Error = &message
	}
	encoder := json.NewEncoder(stdio.Out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		return err
	}
	return cycleErr
}

// serveIsRunning is why once refuses while the lock is held, naming the holder when known.
func serveIsRunning(pid int) error {
	if pid == 0 {
		return errors.New("orch remote serve is running; it syncs on its own")
	}
	return fmt.Errorf("orch remote serve is running; it syncs on its own (pid %d)", pid)
}

// remoteServe runs cycles until SIGINT or SIGTERM, holding the single-instance lock. Log lines
// go to stdout, which launchd writes to remote.log.
func remoteServe(args []string, env remoteEnv, stdio Stdio) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := parseRemoteFlags(flags, args); err != nil {
		return err
	}
	cfg, _, err := loadRemoteSetup(env)
	if err != nil {
		return err
	}
	lock, err := loop.AcquireLock(serveLockFile(env), os.Getpid())
	if err != nil {
		return err
	}
	defer lock.Release()
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals...)
	defer stop()
	newRemoteLoop(env, cfg, stdio.Out).Serve(ctx)
	return nil
}

// serveLockFile is the single-instance lock serve holds while it runs and once for its cycle.
func serveLockFile(env remoteEnv) string {
	return filepath.Join(state.Dir(env.home), "serve.lock")
}

// defaultLaunchdLabel names the LaunchAgent unless --label does.
const defaultLaunchdLabel = "org.opencode-orchestrator.work-trains"

// launchdPath is the agent's PATH: the system tools, security(1) among them, and Homebrew's
// bin directories, where gh lives.
const launchdPath = "/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin"

// launchdLabel is a reverse-DNS style label, safe to print unquoted in a launchctl command.
var launchdLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// remoteLaunchd prints a LaunchAgent plist that runs this orch's `remote serve`, then, on
// stderr, how to install and remove it. It writes nothing.
func remoteLaunchd(args []string, env remoteEnv, stdio Stdio) error {
	flags := flag.NewFlagSet("launchd", flag.ContinueOnError)
	label := flags.String("label", defaultLaunchdLabel, "")
	if err := parseRemoteFlags(flags, args); err != nil {
		return err
	}
	if !launchdLabel.MatchString(*label) {
		return &remoteUsageError{fmt.Sprintf("--label %q must be letters, digits, dots, dashes and underscores", *label)}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logFile := filepath.Join(state.Dir(env.home), "remote.log")
	fmt.Fprint(stdio.Out, launchdPlist(*label, self, logFile))
	plistFile := filepath.Join(env.home, "Library", "LaunchAgents", *label+".plist")
	fmt.Fprintf(stdio.Err, "# Install: save the plist above as %s, then run\n", shellQuote(plistFile))
	fmt.Fprintf(stdio.Err, "#   launchctl bootstrap gui/$(id -u) %s\n", shellQuote(plistFile))
	fmt.Fprintln(stdio.Err, "# Uninstall:")
	fmt.Fprintf(stdio.Err, "#   launchctl bootout gui/$(id -u)/%s\n", *label)
	return nil
}

// launchdPlist is the LaunchAgent that keeps `<orch> remote serve` running, restarted at most
// every 30 s, its output appended to logFile.
func launchdPlist(label, orch, logFile string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlText(label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlText(orch) + `</string>
		<string>remote</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>StandardOutPath</key>
	<string>` + xmlText(logFile) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlText(logFile) + `</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>` + launchdPath + `</string>
	</dict>
</dict>
</plist>
`
}

// shellQuote is s as one word for a POSIX shell, so a copied command keeps a path with spaces
// or shell characters whole: nothing is special inside single quotes, and a single quote is
// written as a closing quote, an escaped quote and an opening one.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func xmlText(text string) string {
	var escaped strings.Builder
	xml.EscapeText(&escaped, []byte(text)) // a strings.Builder never fails a write
	return escaped.String()
}
