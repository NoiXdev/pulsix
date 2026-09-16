// The rotate-key executor (spec §5, backlog 2.7 rotation): it runs the
// embedded key-rotation playbook — installing the new public key over the
// old, still-working credential — against one host or jump host. The
// executor only runs the playbook; the rotation group carries the
// verification: a follow-up connection_test job per host target,
// authenticating with the pending new key sealed in the test job's own
// payload, whose outcome the group's finalize gate counts. The group's
// terminal outcome decides whether the new private key is sealed onto the
// credential or dropped. The new private key travels only inside the
// sealed job payloads and the run's work dir — never an event, stat or
// log.
//
// Terminal mapping is the runner's; broken input is an executor error.
// The host-key pin rides into the runner-shaped claim like the
// bootstrap's: the runner checks it strictly on ansible's own connection.
package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// kindRotateKey is this executor's job kind.
const kindRotateKey = "rotate_key"

// RotateExecutor executes rotate_key jobs by delegating to the worker's
// ansible-runner executor.
type RotateExecutor struct {
	// Runner is the ansible-runner executor the claim reshapes into.
	Runner Executor
}

// NewRotateExecutor wires the executor onto the shared runner executor.
func NewRotateExecutor(runner Executor) *RotateExecutor {
	return &RotateExecutor{Runner: runner}
}

// rotatePayload is the claim's kind payload: the old credential (still
// valid on the target) as the SSH login and the new public key to install.
type rotatePayload struct {
	Host     sysHost `json:"host"`
	Username string  `json:"username"`
	// OldCredential is the credential being rotated, as Secret JSON — the
	// SSH login for this run.
	OldCredential string `json:"old_credential"`
	NewPublicKey  string `json:"new_public_key"`
	// The pending new private key is not part of the claim: the playbook
	// installs the public key only.
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
