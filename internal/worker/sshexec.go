// The connection-test executor (spec §5, backlog 2.7c worker side): it
// parses the claim's kind payload — the self-contained probe target the
// manager resolved at claim time — runs the sshprobe probe against the
// host (through the resolved jump chain, TOFU against the claim's pinned
// key) and emits ONE run event carrying the result. The event's payload is
// the result JSON and nothing else: no credential material ever enters an
// event, a stat or a log line.
//
// Terminal mapping: a test that RAN and reported an outcome is a
// legitimate result — succeeded when everything held, failed otherwise.
// Only broken input (a claim whose payload is not a connection-test
// document) is an executor error, never pretending to have tested.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

// kindConnectionTest is this executor's job kind.
const kindConnectionTest = "connection_test"

// The RunEvent set the manager's recordTest reads: the executor emits one
// event whose state says how the test landed.
const (
	evStateOk     = "ok"
	evStateFailed = "failed"
)

// SSHProbeExecutor executes connection_test jobs with sshprobe.
type SSHProbeExecutor struct {
	probe *sshprobe.Probe
	log   *slog.Logger
}

// NewSSHProbeExecutor wires the executor onto a probe.
func NewSSHProbeExecutor(probe *sshprobe.Probe, log *slog.Logger) *SSHProbeExecutor {
	return &SSHProbeExecutor{probe: probe, log: log}
}

// testPayload is the claim's kind payload: the probe target the manager
// resolved at claim time, with the revealed secrets inline as Secret JSON.
type testPayload struct {
	Host struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Address  string `json:"address"`
		Port     int    `json:"port"`
		Username string `json:"username"`
	} `json:"host"`
	Credential string `json:"credential"`
	Bastions   []struct {
		ID         string `json:"id"`
		Address    string `json:"address"`
		Port       int    `json:"port"`
		Username   string `json:"username"`
		Credential string `json:"credential"`
	} `json:"bastions"`
}

// Run runs the probe and reports the result.
func (e *SSHProbeExecutor) Run(ctx context.Context, job *workerpb.Job, _ RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	if job.GetKind() != kindConnectionTest {
		return StatusError, nil, fmt.Errorf("job kind %q has no ssh-probe executor", job.GetKind())
	}
	var payload testPayload
	if err := json.Unmarshal([]byte(job.GetKindPayload()), &payload); err != nil {
		return StatusError, nil, fmt.Errorf("connection-test kind payload: %w", err)
	}
	if payload.Host.Address == "" || payload.Host.Name == "" {
		return StatusError, nil, fmt.Errorf("connection-test kind payload carries no probe target")
	}
	// The host key the claim pins (if any) — the TOFU input.
	pinned := job.GetHostKeys()[payload.Host.Name]

	secret := credentials.Secret{}
	if payload.Credential != "" {
		if err := json.Unmarshal([]byte(payload.Credential), &secret); err != nil {
			return StatusError, nil, fmt.Errorf("connection-test credential: %w", err)
		}
	}
	target := sshprobe.ProbeTarget{
		Address: payload.Host.Address, Port: payload.Host.Port, Username: payload.Host.Username,
		Credential: secret, PinnedKey: pinned,
	}
	for _, hop := range payload.Bastions {
		hopSecret := credentials.Secret{}
		if hop.Credential != "" {
			if err := json.Unmarshal([]byte(hop.Credential), &hopSecret); err != nil {
				return StatusError, nil, fmt.Errorf("connection-test hop %s credential: %w", hop.ID, err)
			}
			target.Bastions = append(target.Bastions, sshprobe.BastionHop{
				Address: hop.Address, Port: hop.Port, Username: hop.Username, Credential: hopSecret,
			})
		}
	}

	res, err := e.probe.Run(ctx, target)
	if err != nil {
		return StatusError, nil, fmt.Errorf("run connection probe: %w", err)
	}
	// The result JSON is the event's payload — safe by construction
	// (sshprobe.Result carries no credential material).
	raw, err := json.Marshal(res)
	if err != nil {
		return StatusError, nil, fmt.Errorf("encode probe result: %w", err)
	}
	state := evStateOk
	if !res.OK() {
		state = "failed"
	}
	if err := sink(&workerpb.RunEvent{Host: payload.Host.Name, Task: kindConnectionTest, State: state, Payload: string(raw)}); err != nil {
		return StatusError, nil, fmt.Errorf("send connection-test result: %w", err)
	}
	if !res.OK() {
		// A failed test is a legitimate terminal outcome, not an error:
		// the result event and the status carry the outcome for the run
		// view and the host's test history.
		return StatusFailed, map[string]any{"ok": false}, nil
	}
	return StatusSucceeded, map[string]any{"ok": true}, nil
}
