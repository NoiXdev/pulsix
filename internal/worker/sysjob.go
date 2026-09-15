// Shared machinery of the two system-playbook executors (bootstrap and
// rotate_key): both re-shape their self-contained kind payload into the
// one-host claim the ansible-runner executor runs (the same machinery run
// jobs claim — bastion chains, key files, extra vars), and both preflight
// the claim's host-key pin with one SSH handshake before any playbook is
// executed. The preflight exists because a runner run dials with host key
// checking disabled (the pin enforcement is the claim's host_keys, Task
// 10): with a pin on the host, a changed key must abort before anything
// touches the host. Trust on first use stays allowed for unpinned hosts.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/worker/sshprobe"
)

// sysLoginCredKey is the credentials-map key the synthetic claim gives the
// executor's own login (the one-time bootstrap login or the old rotation
// credential). The map is opaque to the worker — the key only has to
// match the connection's credential_id and stay distinct from the hop ids.
const sysLoginCredKey = "pulsix-login"

// sysHost is the kind payload's host object; sysHop one jump of its chain,
// each hop's credential inline as Secret JSON — the same shapes the
// connection-test payload uses.
type sysHost struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

type sysHop struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

// pinPreflight compares one observed host key against the claim's pin,
// mirroring the connection test's TOFU semantics: unpinned hosts pass
// (trust on first use), a mismatch is the executor error that fails the
// job before any playbook runs. seenKey is empty when the handshake never
// got past key exchange — the pin cannot be verified, fail closed.
func pinPreflight(pinned, seenKey, host string) error {
	state := sshprobe.Compare(pinned, seenKey)
	switch state {
	case sshprobe.KeyStateUnpinned, sshprobe.KeyStatePinned:
		return nil
	default: // KeyStateChanged, including "no key seen but a pin exists"
		return fmt.Errorf("host key changed: host %s presented key %q, the pinned key is %s", host, seenKey, pinned)
	}
}

// observeHostKey runs one SSH handshake against the host and returns the
// observed host key fingerprint. Nothing is authenticated and nothing is
// executed: the connection is closed right after key exchange (the
// authentication attempt with no methods fails, which is fine — the key
// was captured during the exchange). The error covers dial and transport
// failures only; a completed exchange always returns the fingerprint.
func observeHostKey(ctx context.Context, address string, port int, timeout time.Duration) (string, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("dial for host key preflight: %w", err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)

	var seenKey string
	cfg := &ssh.ClientConfig{
		// The user and auth list are irrelevant to the exchange: the host
		// key callback fires during key exchange, before any
		// authentication. Accepting every key here is safe — the caller
		// decides the verdict against the pin.
		User: "pulsix-preflight",
		Auth: []ssh.AuthMethod{},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seenKey = ssh.FingerprintSHA256(key)
			return nil
		},
	}
	clientConn, _, _, err := ssh.NewClientConn(conn, net.JoinHostPort(address, strconv.Itoa(port)), cfg)
	if clientConn != nil {
		_ = clientConn.Close()
	}
	// The authentication attempt (no methods configured) fails after the
	// exchange — that error is the success case: the key was captured.
	// Only an exchange that never completed leaves the fingerprint empty.
	if seenKey != "" {
		return seenKey, nil
	}
	if err != nil {
		return "", fmt.Errorf("handshake failed before key exchange: %w", err)
	}
	return "", errors.New("no host key observed")
}

// jsonStr encodes one string as the JSON value form the claim's extra-var
// map carries (the runner's materializeRunnerConfig decodes every value).
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// stripPrivateEnv drops every ANSIBLE_* and PULSIX_* variable: the
// worker's own settings must never leak into a subprocess's environment
// (the runner executor builds the same private environment for runs).
func stripPrivateEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ANSIBLE_") || strings.HasPrefix(name, "PULSIX_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// oneHostClaim assembles the runner-shaped claim for a single target:
// the inventory document with the host's hostvars (and the hop chain's
// ProxyJump args in the renderer's exact spelling, so the runner's
// ssh_config rewrite applies), the connection JSON, and the credentials
// map (the executor's own login under sysLoginCredKey, each hop's under
// its bastion id). loginUser overrides the hostvars' ansible_user.
func oneHostInventory(host sysHost, loginUser, loginSecretJSON string, hops []sysHop) (doc map[string]any, connJSON string, creds map[string]string, err error) {
	vars := map[string]any{
		"ansible_host": host.Address,
		"ansible_port": host.Port,
	}
	if host.Address == "" {
		vars["ansible_host"] = host.Name
	}
	if loginUser != "" {
		vars["ansible_user"] = loginUser
	} else if host.Username != "" {
		vars["ansible_user"] = host.Username
	}

	chain := make([]sshconf.Bastion, 0, len(hops))
	conn := struct {
		HostID               string   `json:"host_id"`
		CredentialID         string   `json:"credential_id,omitempty"`
		Bastions             []string `json:"bastions,omitempty"`
		BastionCredentialIDs []string `json:"bastion_credential_ids,omitempty"`
		BastionSource        string   `json:"bastion_source"`
	}{HostID: host.ID, CredentialID: sysLoginCredKey, BastionSource: "explicit"}
	creds = map[string]string{sysLoginCredKey: loginSecretJSON}
	for _, hop := range hops {
		chain = append(chain, sshconf.Bastion{Address: hop.Address, Port: hop.Port, Username: hop.Username})
		conn.Bastions = append(conn.Bastions, hop.ID)
		conn.BastionCredentialIDs = append(conn.BastionCredentialIDs, hop.ID)
		creds[hop.ID] = hop.Credential
	}
	if len(hops) > 0 {
		vars["ansible_ssh_common_args"] = "-o ProxyJump=" + sshconf.ProxyJump(chain)
	}
	doc = map[string]any{
		"all": map[string]any{"hosts": []string{host.Name}},
		"_meta": map[string]any{"hostvars": map[string]any{
			host.Name: vars,
		}},
	}
	b, err := json.Marshal(conn)
	if err != nil {
		return nil, "", nil, fmt.Errorf("encode connection: %w", err)
	}
	return doc, string(b), creds, nil
}

// synthRunJob clones the claimed job into the runner-shaped claim the
// ansible-runner executor consumes: same id, org and zone, the executor's
// own kind (so the bookends and the playbook materialization stay truthful
// to what ran), and the one-host inventory/connection/credential/extra-var
// material the executor resolved from its kind payload.
func synthRunJob(orig *workerpb.Job, hostName string, doc map[string]any, connJSON string, creds map[string]string, extraVars map[string]string, sshUser string) (*workerpb.Job, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode inventory: %w", err)
	}
	return &workerpb.Job{
		Id: orig.GetId(), Kind: orig.GetKind(),
		OrgId: orig.GetOrgId(), ZoneId: orig.GetZoneId(),
		InventoryDoc: string(raw), Connections: map[string]string{hostName: connJSON},
		Credentials:  creds,
		ExtraVars:    extraVars,
		SshUsername:  sshUser,
		HostKeys:     orig.GetHostKeys(),
	}, nil
}
