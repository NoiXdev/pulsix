// Command agent is the Pulsix host agent (v0.3a): it enrolls with a
// one-time enrollment token on first start, then keeps one AgentStream
// open to the manager — heartbeats (last_seen + version) and host facts.
// The identity lives in the data dir and survives restarts; a reconnect
// never re-enrolls.
//
// Configuration comes from the environment (see internal/agent/config.go
// for the authoritative list and defaults):
//
//	PULSIX_MANAGER_ADDR        manager's gRPC endpoint, host:port (required)
//	PULSIX_MANAGER_SERVER_NAME overrides the TLS ServerName verified against the manager
//	PULSIX_ENROLL_TOKEN        one-time pxa1 token; required only until a valid identity exists
//	PULSIX_AGENT_NAME          agent name (default: the OS hostname)
//	PULSIX_DATA_DIR            identity home, default /etc/pulsix
//	PULSIX_HEARTBEAT_INTERVAL  default 30s (clamped into [5s, 10m])
//	PULSIX_FACTS_INTERVAL      default 6h (clamped into [30m, 24h])
//
// Flags (for Windows and macOS, where there is no journald and the service
// manager brings no environment):
//
//	--env-file <path>  read KEY=VALUE configuration from a file; the process
//	                   environment wins over the file
//	--log-file <path>  write the log to a size-rotated file instead of stderr
//
// Exit codes: 2 on a configuration error (the operator must act — a bad
// config value, a missing token where none is on disk, a refused
// enrollment; each names the fix), 0 on a clean shutdown
// (SIGTERM/SIGINT), 1 on anything else. Manager downtime is deliberately
// NOT an error exit: the loop waits it out with backoff; systemd's
// Restart=on-failure covers crashes, and the service keeps running (and
// reconnecting) through everything an operator would not have to fix.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/noixdev/pulsix/internal/agent"
	"github.com/noixdev/pulsix/internal/agent/facts"
	"github.com/noixdev/pulsix/internal/agent/logfile"
)

// version is the agent's build version, reported on every heartbeat and
// enrollment so an operator can tell exactly what is running. It stays
// "dev" for a local `go build`; the release build (Makefile's
// agent-binaries target, the manager Dockerfile) stamps it with
// `-ldflags "-X main.version=..."`, the same mechanism cmd/manager and
// cmd/worker use.
var version = "dev"

// collector adapts the facts package's collection function to the loop's
// Collector port.
type collector struct{}

func (collector) Collect(ctx context.Context) (map[string]string, error) {
	return facts.Collect(ctx)
}

// main only translates runPlatform's exit code; the indirection keeps run's
// deferred closes executing (os.Exit from inside a function with pending
// defers skips them).
func main() {
	os.Exit(runPlatform(os.Args[1:], os.Environ()))
}

// run is the foreground entrypoint: it cancels the agent on SIGTERM
// (systemd's and launchd's stop) or SIGINT (Ctrl-C during local
// development).
func run(args, env []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return runWithContext(ctx, args, env)
}

// runWithContext wires the agent process: parse the flags, open the log
// file, load the env file, load the configuration, run the loop until ctx
// ends and map the outcome to the exit code.
func runWithContext(ctx context.Context, args, env []string) int {
	fs := flag.NewFlagSet("pulsix-agent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	envFile := fs.String("env-file", "", "read configuration from this KEY=VALUE file")
	logPath := fs.String("log-file", "", "write the log to this file instead of stderr")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "pulsix-agent:", err)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "pulsix-agent: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	var out io.Writer = os.Stderr
	if *logPath != "" {
		w, err := logfile.Open(*logPath, logfile.DefaultMaxBytes)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pulsix-agent: open log file:", err)
			return 2
		}
		defer func() { _ = w.Close() }()
		out = w
	}
	logger := slog.New(slog.NewTextHandler(out, nil))

	if *envFile != "" {
		fileEnv, err := agent.LoadEnvFile(*envFile)
		if err != nil {
			logger.Error("configuration error", "err", err)
			return 2
		}
		env = agent.MergeEnv(env, fileEnv)
	}

	cfg, err := agent.Load(env)
	if err != nil {
		logger.Error("configuration error", "err", err)
		return 2
	}
	cfg.Version = version
	facts.AgentVersion = version

	return runAgent(ctx, cfg, logger)
}

// runAgent runs the loop until ctx ends and maps the outcome to the exit
// code: 0 clean, 1 failure, 2 operator action needed.
func runAgent(ctx context.Context, cfg agent.Config, logger *slog.Logger) int {
	logger.Info("pulsix-agent starting", "manager", cfg.ManagerAddr, "data_dir", cfg.DataDir)
	loop := agent.NewLoop(cfg, agent.NewDialer(cfg), collector{}, agent.WithLogger(logger))
	if err := loop.Run(ctx); err != nil {
		// Both enrollment sentinels carry the remediation ("create a new
		// enrollment token"); the loop has already logged the details. A
		// local setup failure (read-only data dir, unparseable key) names
		// its own fix — all three are operator actions, hence exit 2.
		if errors.Is(err, agent.ErrNoEnrollment) || errors.Is(err, agent.ErrEnrollRefused) ||
			errors.Is(err, agent.ErrSetupFailed) {
			logger.Error("agent needs operator attention", "err", err)
			return 2
		}
		logger.Error("agent loop stopped", "err", err)
		return 1
	}
	return 0
}
