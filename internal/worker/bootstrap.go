// The bootstrap executor (spec §5, backlog 2.7c part 2): it runs the
// embedded account bootstrap playbook (create the ansible user, install
// the host credential's public key, grant passwordless sudo) against one
// host, authenticating with the operator-provided one-time login the
// manager resolved into the claim at enqueue time. That login is used once
// and never stored anywhere — it travels inside the sealed job payload and
// dies with the run's work dir.
//
// Terminal mapping is the runner's: the playbook ran and reported an
// outcome → succeeded/failed with the runner's stats; broken input (a
// claim whose payload is not a bootstrap document) is an executor error.
// The host-key pin is preflighted before anything runs: a changed key
// fails the job (the run itself dials with host-key checking disabled).
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

// kindBootstrap is this executor's job kind.
const kindBootstrap = "bootstrap"

// BootstrapExecutor executes bootstrap jobs by delegating to the worker's
// ansible-runner executor (wired in cmd/worker/main.go): the inventory
// materialization, the event streaming, the stats and the terminal
// mapping are the run jobs'.
type BootstrapExecutor struct {
	// Runner is the ansible-runner executor the claim reshapes into.
	Runner Executor
	// KeyTimeout bounds the pin preflight's dial and exchange;
	// sshprobe's default when zero.
	KeyTimeout time.Duration
}

// NewBootstrapExecutor wires the executor onto the shared runner executor.
func NewBootstrapExecutor(runner Executor) *BootstrapExecutor {
	return &BootstrapExecutor{Runner: runner}
}

// bootstrapPayload is the claim's kind payload: the one-time login and
// everything the run needs, resolved by the manager at claim time.
type bootstrapPayload struct {
	Host      sysHost `json:"host"`
	LoginUser string  `json:"login_user"`
	// Login is the one-time credential as Secret JSON.
	Login      string   `json:"login"`
	PublicKey  string   `json:"public_key"`
	PulsixUser string   `json:"pulsix_user"`
	Bastions   []sysHop `json:"bastions"`
}

// Run preflights the pin, reshapes the claim and delegates to the runner.
func (e *BootstrapExecutor) Run(ctx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	if job.GetKind() != kindBootstrap {
		return StatusError, nil, fmt.Errorf("job kind %q has no bootstrap executor", job.GetKind())
	}
	var payload bootstrapPayload
	if err := json.Unmarshal([]byte(job.GetKindPayload()), &payload); err != nil {
		return StatusError, nil, fmt.Errorf("bootstrap kind payload: %w", err)
	}
	if payload.Host.Address == "" || payload.Host.Name == "" || payload.Login == "" || payload.PublicKey == "" {
		return StatusError, nil, fmt.Errorf("bootstrap kind payload carries no target, one-time login or public key")
	}

	// The host key the claim pins (if any): with a pin on record, a
	// changed key aborts the bootstrap before anything touches the host.
	// Trust on first use stays the operator-initiated default for
	// unpinned hosts.
	if pinned := job.GetHostKeys()[payload.Host.Name]; pinned != "" {
		seen, err := observeHostKey(ctx, payload.Host.Address, payload.Host.Port, e.keyTimeout())
		if err != nil {
			return StatusError, nil, fmt.Errorf("bootstrap host-key preflight: %w", err)
		}
		if err := pinPreflight(pinned, seen, payload.Host.Name); err != nil {
			return StatusError, nil, err
		}
	}

	doc, connJSON, creds, err := oneHostInventory(payload.Host, payload.LoginUser, payload.Login, payload.Bastions)
	if err != nil {
		return StatusError, nil, err
	}
	synth, err := synthRunJob(job, payload.Host.Name, doc, connJSON, creds, map[string]string{
		"pulsix_public_key": jsonStr(payload.PublicKey),
		"pulsix_user":       jsonStr(payload.PulsixUser),
	}, payload.LoginUser)
	if err != nil {
		return StatusError, nil, err
	}
	return e.Runner.Run(ctx, synth, env, sink)
}

func (e *BootstrapExecutor) keyTimeout() time.Duration {
	if e.KeyTimeout > 0 {
		return e.KeyTimeout
	}
	return sshprobe.DefaultDialTimeout
}
