// The revoke-key executor (final review I1): the last phase of a key
// rotation. It runs the embedded revocation playbook — removing the
// credential's previous public key from the account's authorized_keys —
// against one host or jump host, logging in with the credential's current
// key (the new one, sealed onto the credential when the rotation group was
// verified everywhere). The manager enqueues revocations only after that
// verification, so a rotation that did not reach every target never removes
// the key the other targets still depend on.
//
// Terminal mapping is the runner's; broken input is an executor error. The
// host-key pin rides into the runner-shaped claim like the rotation's.
package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// kindRevokeKey is this executor's job kind.
const kindRevokeKey = "revoke_key"

// RevokeExecutor executes revoke_key jobs by delegating to the worker's
// ansible-runner executor.
type RevokeExecutor struct {
	// Runner is the ansible-runner executor the claim reshapes into.
	Runner Executor
}

// NewRevokeExecutor wires the executor onto the shared runner executor.
func NewRevokeExecutor(runner Executor) *RevokeExecutor {
	return &RevokeExecutor{Runner: runner}
}

// revokePayload is the claim's kind payload: the target, the account, the
// credential's current key as the login, and the previous public key.
type revokePayload struct {
	Host     sysHost `json:"host"`
	Username string  `json:"username"`
	// LoginCredential is the credential's current secret as Secret JSON.
	LoginCredential string   `json:"login_credential"`
	OldPublicKey    string   `json:"old_public_key"`
	PulsixUser      string   `json:"pulsix_user"`
	Bastions        []sysHop `json:"bastions"`
}

// Run runs the revocation playbook for the job's single target.
func (e *RevokeExecutor) Run(ctx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	if job.GetKind() != kindRevokeKey {
		return StatusError, nil, fmt.Errorf("job kind %q has no revoke-key executor", job.GetKind())
	}
	var payload revokePayload
	if err := json.Unmarshal([]byte(job.GetKindPayload()), &payload); err != nil {
		return StatusError, nil, fmt.Errorf("revoke-key kind payload: %w", err)
	}
	if payload.Host.Address == "" || payload.Host.Name == "" || payload.LoginCredential == "" || payload.OldPublicKey == "" {
		return StatusError, nil, fmt.Errorf("revoke-key kind payload carries no target, login or previous public key")
	}
	loginUser := payload.Username
	if loginUser == "" {
		loginUser = payload.Host.Username
	}
	pulsixUser := payload.PulsixUser
	if pulsixUser == "" {
		pulsixUser = loginUser
	}
	doc, connJSON, creds, err := oneHostInventory(payload.Host, loginUser, payload.LoginCredential, payload.Bastions)
	if err != nil {
		return StatusError, nil, err
	}
	synth, err := synthRunJob(job, payload.Host.Name, doc, connJSON, creds, map[string]string{
		"pulsix_old_public_key": jsonStr(payload.OldPublicKey),
		"pulsix_user":           jsonStr(pulsixUser),
	}, loginUser)
	if err != nil {
		return StatusError, nil, err
	}
	return e.Runner.Run(ctx, synth, env, sink)
}
