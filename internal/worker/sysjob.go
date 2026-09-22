// Shared machinery of the two system-playbook executors (bootstrap and
// rotate_key): both re-shape their self-contained kind payload into the
// one-host claim the ansible-runner executor runs (the same machinery run
// jobs claim — bastion chains, key files, extra vars, and the host-key
// pins). There is no separate preflight dial: the claim's pin (host_keys
// plus the full key in known_hosts) rides into the runner-shaped claim, and
// the runner enforces it with strict host key checking on ansible's own
// connection through the host's jump chain — the connection the playbook
// actually uses (final review C2). Unpinned hosts are trusted on first use
// inside the run's wiped known_hosts.
package worker

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
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
		Credentials:       creds,
		ExtraVars:         extraVars,
		SshUsername:       sshUser,
		HostKeys:          orig.GetHostKeys(),
		KnownHosts:        orig.GetKnownHosts(),
		BastionHostKeys:   orig.GetBastionHostKeys(),
		BastionKnownHosts: orig.GetBastionKnownHosts(),
		Redact:            orig.GetRedact(),
	}, nil
}
