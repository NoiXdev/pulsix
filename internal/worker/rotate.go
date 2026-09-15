// The rotate-key executor (spec §5, backlog 2.7 rotation): it runs the
// embedded key-rotation playbook — installing the new public key over the
// old, still-working credential — against one host or jump host. The
// executor only runs the playbook; the enqueue side (credentials.Rotate)
// appends the connection test that verifies the new key, and the group's
// terminal outcome decides whether the new private key is sealed onto the
// credential or dropped. The new private key travels only inside the
// sealed job payload and the run's work dir — never an event, stat or log.
//
// Terminal mapping is the runner's; broken input is an executor error.
// The host-key pin is preflighted like the bootstrap's: with a pin on
// record, a changed key aborts before anything runs.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

// kindRotateKey is this executor's job kind.
const kindRotateKey = "rotate_key"

// RotateExecutor executes rotate_key jobs by delegating to the worker's
// ansible-runner executor.
type RotateExecutor struct {
	// Runner is the ansible-runner executor the claim reshapes into.
	Runner Executor
	// KeyTimeout bounds the pin preflight's dial and exchange;
	// sshprobe's default when zero.
	KeyTimeout time.Duration
}

// NewRotateExecutor wires the executor onto the shared runner executor.
func NewRotateExecutor(runner Executor) *RotateExecutor {
	return &RotateExecutor{Runner: runner}
}

// rotatePayload is the claim's kind payload: the old credential (still
// valid on the target) as the SSH login, the new public key to install,
// and the pending new private key that rides sealed in the job payload.
type rotatePayload struct {
	Host     sysHost `json:"host"`
	Username string  `json:"username"`
	// OldCredential is the credential being rotated, as Secret JSON — the
	// SSH login for this run.
	OldCredential string `json:"old_credential"`
	NewPublicKey  string `json:"new_public_key"`
	// NewPrivateKey is the pending key as Secret JSON ({"private_key":
	// "…"}); consumed only by the manager's finalize step, never by this
	// run.
	NewPrivateKey  string   `json:"new_private_key"`
	NewFingerprint string   `json:"new_fingerprint"`
	NewKeyType     string   `json:"new_key_type"`
	PulsixUser     string   `json:"pulsix_user"`
	Bastions       []sysHop `json:"bastions"`
}

// Run runs the rotation playbook for the job's single target.
func (e *RotateExecutor) Run(ctx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	if job.GetKind() != kindRotateKey {
		return StatusError, nil, fmt.Errorf("job kind %q has no rotate-key executor", job.GetKind())
	}
	var payload rotatePayload
	if err := json.Unmarshal([]byte(job.GetKindPayload()), &payload); err != nil {
		return StatusError, nil, fmt.Errorf("rotate-key kind payload: %w", err)
	}
	if payload.Host.Address == "" || payload.Host.Name == "" || payload.OldCredential == "" || payload.NewPublicKey == "" {
		return StatusError, nil, fmt.Errorf("rotate-key kind payload carries no target, old credential or new public key")
	}

	// The host key the claim pins (if any): with a pin on record, a
	// changed key aborts the rotation before anything touches the host.
	if pinned := job.GetHostKeys()[payload.Host.Name]; pinned != "" {
		seen, err := observeHostKey(ctx, payload.Host.Address, payload.Host.Port, e.keyTimeout())
		if err != nil {
			return StatusError, nil, fmt.Errorf("rotate-key host-key preflight: %w", err)
		}
		if err := pinPreflight(pinned, seen, payload.Host.Name); err != nil {
			return StatusError, nil, err
		}
	}

	loginUser := payload.Username
	if loginUser == "" {
		loginUser = payload.Host.Username
	}
	doc, connJSON, creds, err := oneHostInventory(payload.Host, loginUser, payload.OldCredential, payload.Bastions)
	if err != nil {
		return StatusError, nil, err
	}
	synth, err := synthRunJob(job, payload.Host.Name, doc, connJSON, creds, map[string]string{
		"pulsix_public_key": jsonStr(payload.NewPublicKey),
		"pulsix_user":       jsonStr(loginUser),
	}, loginUser)
	if err != nil {
		return StatusError, nil, err
	}
	return e.Runner.Run(ctx, synth, env, sink)
}

func (e *RotateExecutor) keyTimeout() time.Duration {
	if e.KeyTimeout > 0 {
		return e.KeyTimeout
	}
	return sshprobe.DefaultDialTimeout
}
