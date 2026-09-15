package runner

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// connection is the claim's per-host connection JSON: the rendered
// Connection (internal/inventory/render) plus the claim-time additions the
// payload build ships — the EFFECTIVE login credential (the host's own, or
// the template's default) and, aligned with the bastions list, each
// bastion's login credential id.
type connection struct {
	HostID               string   `json:"host_id"`
	CredentialID         string   `json:"credential_id,omitempty"`
	Bastions             []string `json:"bastions,omitempty"`
	BastionCredentialIDs []string `json:"bastion_credential_ids,omitempty"`
	BastionSource        string   `json:"bastion_source"`
}

// mergeInventory folds the claim's connection material into the rendered
// inventory document, returning the rewritten document, the ssh_config Host
// blocks for every bastion hop that resolves to a key credential, and the
// credential ids whose private key the runner must write to keysDir.
//
// It changes only hostvars the claim fully explains: a jump chain whose
// hops cannot be resolved (missing credential ids, a password-only bastion,
// a chain that does not match its rendering) stays exactly as rendered —
// an operator-facing value is never mangled into something worse. A broken
// claim (bad connection JSON, unopenable credential) fails the merge before
// any file is written.
func mergeInventory(doc map[string]any, connections map[string]string, secrets map[string]string, sshUser, keysDir string) (map[string]any, []proxyHost, []string, error) {
	meta, _ := doc["_meta"].(map[string]any)
	hostvars, _ := meta["hostvars"].(map[string]any)
	var hosts []proxyHost
	keySet := map[string]bool{}
	bastionsSeen := map[string]bool{}

	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		var conn connection
		if err := json.Unmarshal([]byte(connections[name]), &conn); err != nil {
			return nil, nil, nil, fmt.Errorf("connection %s: %w", name, err)
		}
		vars, ok := hostvars[name].(map[string]any)
		if !ok {
			continue // a host the snapshot does not render: leave it out
		}
		secret, err := credentialSecret(secrets, conn.CredentialID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("credential of host %s: %w", name, err)
		}
		if secret.PrivateKey != "" && !hasVar(vars, "ansible_ssh_private_key_file") {
			keySet[conn.CredentialID] = true
			vars["ansible_ssh_private_key_file"] = keyPath(keysDir, conn.CredentialID)
		}
		if secret.Password != "" && !hasVar(vars, "ansible_password") {
			vars["ansible_password"] = secret.Password
		}
		if _, ok := vars["ansible_user"]; !ok && sshUser != "" {
			vars["ansible_user"] = sshUser
		}

		args := varString(vars, "ansible_ssh_common_args")
		parts, chain, ok := parseProxyJump(args)
		if !ok || !aligned(conn, len(parts)) {
			continue
		}
		var blocks []proxyHost
		var aliases []string
		for i, bastionID := range conn.Bastions {
			credID := conn.BastionCredentialIDs[i]
			secret, err := credentialSecret(secrets, credID)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("credential of bastion %s: %w", bastionID, err)
			}
			if secret.PrivateKey == "" {
				// A credential-less or password-only hop cannot be
				// scripted (ssh has no ProxyJump password entry); the
				// whole chain stays as rendered rather than half-rewritten.
				blocks = nil
				break
			}
			keySet[credID] = true
			blocks = append(blocks, hopHost(bastionID, parts[i], keyPath(keysDir, credID)))
			aliases = append(aliases, bastionAlias(bastionID))
		}
		if blocks == nil {
			continue
		}
		rewritten, ok := rewriteProxyJump(args, chain, strings.Join(aliases, ","))
		if !ok {
			continue
		}
		vars["ansible_ssh_common_args"] = rewritten
		for _, h := range blocks {
			if !bastionsSeen[h.id] {
				bastionsSeen[h.id] = true
				hosts = append(hosts, h)
			}
		}
	}
	return doc, hosts, slices.Sorted(maps.Keys(keySet)), nil
}

// chainResolves reports whether the connection's hop lists line up with the
// rendered chain.
func aligned(conn connection, hops int) bool {
	return len(conn.Bastions) == hops && len(conn.BastionCredentialIDs) == hops
}

// credentialSecret parses one claim credential's Secret JSON. A missing
// credential id (a host or hop without one) is the zero secret, not an
// error; a referenced credential the claim does not carry, or whose secret
// is not valid JSON, fails the merge.
func credentialSecret(secrets map[string]string, id string) (credentials.Secret, error) {
	if id == "" {
		return credentials.Secret{}, nil
	}
	raw, ok := secrets[id]
	if !ok {
		return credentials.Secret{}, fmt.Errorf("credential %s is not in the claim", id)
	}
	var s credentials.Secret
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return credentials.Secret{}, fmt.Errorf("secret %s: %w", id, err)
	}
	return s, nil
}

// hasVar reports whether the hostvars already carry the variable.
func hasVar(vars map[string]any, name string) bool {
	_, ok := vars[name]
	return ok
}

// varString reads a string-typed hostvar.
func varString(vars map[string]any, name string) string {
	if s, ok := vars[name].(string); ok {
		return s
	}
	return ""
}

// keyPath is where the runner writes a credential's private key file.
func keyPath(keysDir, credID string) string {
	return filepath.Join(keysDir, credID)
}

// materializeInventory writes the run's inventory: the claim's dynamic
// inventory document with the connection secrets folded in (inventory.json,
// 0600), the private ssh_config for the bastion hops, the per-credential
// key files (0600, one per credential the connections reference), and the
// executable wrapper the run passes as --inventory.
func (r *Runner) materializeInventory(job *workerpb.Job, env worker.RunEnv) error {
	var doc map[string]any
	if err := json.Unmarshal([]byte(job.GetInventoryDoc()), &doc); err != nil {
		return fmt.Errorf("inventory document: %w", err)
	}
	keysDir := filepath.Join(env.WorkDir, keysDirName)
	merged, blocks, keys, err := mergeInventory(doc, job.GetConnections(), job.GetCredentials(), job.GetSshUsername(), keysDir)
	if err != nil {
		return err
	}
	invJSON, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encode inventory document: %w", err)
	}
	if err := writeTextFile(filepath.Join(env.WorkDir, inventoryDocName), string(invJSON), 0o600); err != nil {
		return err
	}
	if err := writeTextFile(filepath.Join(env.WorkDir, sshConfigName), sshConfigText(blocks), 0o600); err != nil {
		return err
	}
	for _, id := range keys {
		secret, err := credentialSecret(job.GetCredentials(), id)
		if err != nil {
			return fmt.Errorf("credential %s: %w", id, err)
		}
		if err := writeSecretFile(keyPath(keysDir, id), secret.PrivateKey, 0o600); err != nil {
			return err
		}
	}
	return writeTextFile(
		filepath.Join(env.WorkDir, inventoryWrapperName),
		inventoryWrapperText(filepath.Join(env.WorkDir, inventoryDocName)),
		0o700,
	)
}

// materializeRunnerConfig writes the run's ansible.cfg (0600), the extra
// vars document (env/extravars, 0600 — ansible-runner feeds it as
// -e @env/extravars, so the values never pass through the command line)
// and the scratch directories.
func (r *Runner) materializeRunnerConfig(job *workerpb.Job, env worker.RunEnv) error {
	if err := writeTextFile(filepath.Join(env.WorkDir, ansibleCfgName), ansibleCfgText(env.WorkDir), 0o600); err != nil {
		return err
	}
	if extraVars := job.GetExtraVars(); len(extraVars) > 0 {
		vars := make(map[string]any, len(extraVars))
		for k, v := range extraVars {
			// The claim's extra var values are JSON-encoded; a value that
			// is not JSON fails the run instead of being pasted into the
			// document.
			var raw any
			if err := json.Unmarshal([]byte(v), &raw); err != nil {
				return fmt.Errorf("extra var %s: %w", k, err)
			}
			vars[k] = raw
		}
		b, err := json.Marshal(vars)
		if err != nil {
			return fmt.Errorf("encode extra vars: %w", err)
		}
		if err := writeTextFile(filepath.Join(env.WorkDir, envDirName, extraVarsName), string(b), 0o600); err != nil {
			return err
		}
	}
	return nil
}
