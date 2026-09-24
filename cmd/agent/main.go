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
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/noixdev/pulsix/internal/agent"
	"github.com/noixdev/pulsix/internal/agent/facts"
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

// main only translates run's exit code; the indirection keeps run's
// deferred closes executing (os.Exit from inside a function with pending
// defers skips them).
func main() {
	os.Exit(run(os.Environ()))
}

// run wires the agent process: load the configuration, run the loop under
// the signal context, map the outcome to the exit code.
func run(env []string) int {
	cfg, err := agent.Load(env)
	if err != nil {
		slog.Error("configuration error", "err", err)
		return 2
	}
	cfg.Version = version
	facts.AgentVersion = version

	// Cancelled on SIGTERM (systemd's stop) or SIGINT (Ctrl-C during
	// local development); the loop shuts down cleanly on either.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	slog.Info("pulsix-agent starting", "manager", cfg.ManagerAddr, "data_dir", cfg.DataDir)
	loop := agent.NewLoop(cfg, agent.NewDialer(cfg), collector{}, agent.WithLogger(slog.Default()))
	if err := loop.Run(ctx); err != nil {
		// Both enrollment sentinels carry the remediation ("create a new
		// enrollment token"); the loop has already logged the details. A
		// local setup failure (read-only data dir, unparseable key) names
		// its own fix — all three are operator actions, hence exit 2.
		if errors.Is(err, agent.ErrNoEnrollment) || errors.Is(err, agent.ErrEnrollRefused) ||
			errors.Is(err, agent.ErrSetupFailed) {
			slog.Error("agent needs operator attention", "err", err)
			return 2
		}
		slog.Error("agent loop stopped", "err", err)
		return 1
	}
	return 0
}
