package runner

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/vaultlabel"
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
// bastionPins maps the pinned bastions' ids to their pinned fingerprints
// (backlog 2.40): a pinned hop's block demands the key — and a route that
// dials a pinned bastion but cannot be fully aliased (a hop anywhere on it
// whose credential cannot be scripted, a chain that does not match the
// claim, or a failed rewrite) fails the merge closed, because the raw chain
// would reach the pinned hop under "Host *" with accept-new (backlog 7.14).
//
// It changes only hostvars the claim fully explains: a jump chain whose
// hops cannot be resolved (missing credential ids, a password-only bastion,
// a chain that does not match its rendering) on a route with no pinned
// bastion stays exactly as rendered — an operator-facing value is never mangled into something
// worse. A broken claim (bad connection JSON, unopenable credential) fails
// the merge before any file is written.
func mergeInventory(doc map[string]any, connections map[string]string, secrets map[string]string, sshUser, keysDir string, bastionPins map[string]string) (map[string]any, []proxyHost, []string, error) {
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
		if !ok {
			continue // the args dial no jump host at all
		}
		pinned := pinnedHop(conn, bastionPins)
		if !aligned(conn, len(parts)) {
			if pinned != "" {
				return nil, nil, nil, fmt.Errorf("host %s: the route dials pinned bastion %s but its jump chain does not match the claim, so the pin could never be enforced", name, pinned)
			}
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
				// whole chain stays as rendered rather than half-rewritten
				// — unless the route dials a pinned bastion anywhere: the
				// raw chain reaches every hop by address under "Host *"
				// with accept-new, so no pin on it could be enforced. Fail
				// closed (backlog 2.40 for the pinned hop itself, 7.14 for
				// a pinned hop elsewhere on the route).
				if pinned != "" {
					return nil, nil, nil, fmt.Errorf("host %s: the route dials pinned bastion %s but hop %s cannot be scripted (password-only or none), so the chain cannot be aliased and the pin could never be enforced: give every hop of the route an ssh_key credential", name, pinned, bastionID)
				}
				blocks = nil
				break
			}
			keySet[credID] = true
			blocks = append(blocks, hopHost(bastionID, parts[i], keyPath(keysDir, credID), bastionPins[bastionID]))
			aliases = append(aliases, bastionAlias(bastionID))
		}
		if blocks == nil {
			continue
		}
		rewritten, ok := rewriteProxyJump(args, chain, strings.Join(aliases, ","))
		if !ok {
			if pinned != "" {
				return nil, nil, nil, fmt.Errorf("host %s: the route dials pinned bastion %s but its jump chain could not be rewritten to the pinned aliases, so the pin could never be enforced", name, pinned)
			}
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

// pinnedHop returns the first pinned bastion the connection's route dials,
// "" when none is.
func pinnedHop(conn connection, bastionPins map[string]string) string {
	for _, id := range conn.Bastions {
		if bastionPins[id] != "" {
			return id
		}
	}
	return ""
}

// aligned reports whether the connection's hop lists line up with the
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
// inventory document with the connection secrets and the pinned hosts'
// strict host-key options folded in (inventory.json, 0600), the run's
// known_hosts with the pinned keys (0600), the private ssh_config for the
// bastion hops, the per-credential key files (0600, one per credential the
// connections reference), and the executable wrapper the run passes as
// --inventory.
func (r *Runner) materializeInventory(job *workerpb.Job, env worker.RunEnv) error {
	var doc map[string]any
	if err := json.Unmarshal([]byte(job.GetInventoryDoc()), &doc); err != nil {
		return fmt.Errorf("inventory document: %w", err)
	}
	keysDir := filepath.Join(env.WorkDir, keysDirName)
	merged, blocks, keys, err := mergeInventory(doc, job.GetConnections(), job.GetCredentials(), job.GetSshUsername(), keysDir, job.GetBastionHostKeys())
	if err != nil {
		return err
	}
	knownHosts, err := pinHosts(merged, job.GetConnections(), job.GetHostKeys(), job.GetKnownHosts())
	if err != nil {
		return err
	}
	// The pinned jump hosts' lines ride the same run's known_hosts (backlog
	// 2.40) — the hop blocks demand their keys.
	bastionLines, err := pinBastions(job.GetConnections(), job.GetBastionHostKeys(), job.GetBastionKnownHosts())
	if err != nil {
		return err
	}
	knownHosts += bastionLines
	moveCommonArgs(merged)
	invJSON, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encode inventory document: %w", err)
	}
	if err := writeTextFile(filepath.Join(env.WorkDir, inventoryDocName), string(invJSON), 0o600); err != nil {
		return err
	}
	knownHostsPath := filepath.Join(env.WorkDir, knownHostsName)
	if err := writeTextFile(knownHostsPath, knownHosts, 0o600); err != nil {
		return err
	}
	if err := writeTextFile(filepath.Join(env.WorkDir, sshConfigName), sshConfigText(blocks, knownHostsPath), 0o600); err != nil {
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
	vaultFiles, err := r.materializeVaultPassword(job, env)
	if err != nil {
		return err
	}
	if err := writeTextFile(filepath.Join(env.WorkDir, ansibleCfgName), ansibleCfgText(env.WorkDir, vaultFiles), 0o600); err != nil {
		return err
	}
	// The run's HOME has to exist, owner-only like the rest of the work dir
	// until ShareWorkDir opens it to the play's group: without it
	// ansible-core prints "Failed to create the directory …/home/.ansible"
	// to stdout on every run, which the event stream can only skip as a
	// broken line (backlog 2.115). Repository-backed runs created it
	// already, for the fetch's ssh_config.
	if err := os.MkdirAll(filepath.Join(env.WorkDir, homeDirName), 0o700); err != nil {
		return fmt.Errorf("create the run's home: %w", err)
	}
	extraVars := job.GetExtraVars()
	vars := make(map[string]any, len(extraVars)+len(forcedConnectionVars(env.WorkDir)))
	for k, v := range extraVars {
		// A claim must not set the connection-security variables the run
		// forces below (the manager refuses them; this is the worker's own
		// guard, re-review N1). System jobs' pulsix_ inputs are exempt;
		// the variable carrying the pinned options is not one of them.
		if ansiblevars.Forbidden(k) && !strings.HasPrefix(k, "pulsix_") {
			return fmt.Errorf("extra var %s is a reserved name (connection security, set by the worker, or Ansible's own markers); refusing the run", k)
		}
		// The claim's extra var values are JSON-encoded; a value that is
		// not JSON fails the run instead of being pasted into the document.
		var raw any
		if err := json.Unmarshal([]byte(v), &raw); err != nil {
			return fmt.Errorf("extra var %s: %w", k, err)
		}
		vars[k] = raw
	}
	for k, v := range forcedConnectionVars(env.WorkDir) {
		vars[k] = v
	}
	b, err := guardedExtraVars(vars)
	if err != nil {
		return err
	}
	return writeTextFile(filepath.Join(env.WorkDir, envDirName, extraVarsName), string(b), 0o600)
}

// guardedExtraVars encodes the extra-vars document with the guard
// (extraVarsGuardVar) as its very first key, whatever the other names sort
// as. When Ansible cannot load the file its error quotes the head of line
// 1 — the whole one-line document — and redaction only replaces whole
// values, so anything but the guard there could show a prefix of a secret
// value (final re-review 3). A claim's own entry under the guard's name is
// dropped.
func guardedExtraVars(vars map[string]any) ([]byte, error) {
	delete(vars, extraVarsGuardVar)
	guard, err := json.Marshal(map[string]any{extraVarsGuardVar: map[string]any{extraVarsGuardKey: true}})
	if err != nil {
		return nil, fmt.Errorf("encode extra vars guard: %w", err)
	}
	rest, err := json.Marshal(vars)
	if err != nil {
		return nil, fmt.Errorf("encode extra vars: %w", err)
	}
	// guard is `{"pulsix_extravars_guard":{…}}` and rest `{…}`: splice them
	// into one object, the guard's member first.
	out := guard[:len(guard)-1]
	if len(rest) > 2 {
		out = append(append(out, ','), rest[1:]...)
	} else {
		out = append(out, '}')
	}
	return out, nil
}

// extraVarsGuardVar holds a key YAML cannot parse but JSON can: YAML caps
// an implicit (flow-mapping) key at 1024 characters, and both PyYAML and
// libyaml refuse a longer one. ansible-core loads "-e @file" as JSON and,
// when that fails for any reason (a reserved "__ansible" key, nesting past
// Python's recursion limit, …), silently re-reads the file as YAML — where
// every {"__ansible_unsafe": …} marker is a plain dict whose strings are
// templated (final re-reviews 1 and 2). With the guard in the document the
// YAML re-read fails too, so a failed JSON load fails the run instead of
// running a webhook's lookup. The manager's own checks (depth cap, reserved
// keys) keep such documents from being built; this is the worker's line.
const extraVarsGuardVar = "pulsix_extravars_guard"

// extraVarsGuardKey is the guard's over-long key (1100 characters).
var extraVarsGuardKey = func() string {
	const text = "json-only: this key is longer than YAML's 1024-character limit for implicit keys "
	return text + strings.Repeat(".", 1100-len(text))
}()

// pinnedCommonArgsVar is the hostvar each host's SSH common args (the
// pinned host-key options and the jump chain) move into; the forced
// ansible_ssh_common_args extra var reads it back per host.
//
// The ansible_ssh_ prefix is what protects it. Ansible injects the facts a
// module returns as top-level vars, above inventory hostvars, and the
// managed host writes that result. ansible-core's clean_facts drops every
// returned fact named like a connection plugin's variable (^ansible_ssh_,
// except ansible_ssh_host_key_*), so a host cannot shadow this one. It was
// pulsix_ssh_common_args, which clean_facts does not know: a compromised
// host could return it as a fact, drop its own pin on the next connection
// and add a ProxyCommand that ran on the worker (backlog 7.28).
const pinnedCommonArgsVar = "ansible_ssh_pulsix_common_args"

// forcedConnectionVars are the connection-security variables every run
// forces as extra vars — Ansible's highest precedence, above inventory,
// play and task vars — so nothing in the inventory or a playbook's vars
// can switch host key checking off or put other SSH options ahead of the
// pinned ones (OpenSSH keeps the first value it sees). The per-host common
// args ride a template reference to pinnedCommonArgsVar, because extra
// vars are global. What stays possible is a playbook redefining that
// variable itself in its own vars: playbook authors run code
// on the worker by design (spec §14). The paramiko connection's checking
// is forced on as well; its known hosts file is the run's empty home, so
// a paramiko connection fails closed.
func forcedConnectionVars(workDir string) map[string]any {
	return map[string]any{
		"ansible_ssh_args":                   sshArgs(workDir),
		"ansible_host_key_checking":          true,
		"ansible_ssh_host_key_checking":      true,
		"ansible_paramiko_host_key_checking": true,
		"ansible_ssh_common_args":            "{{ " + pinnedCommonArgsVar + " | default('') }}",
		"ansible_ssh_extra_args":             "",
		"ansible_ssh_executable":             "ssh",
	}
}

// moveCommonArgs moves every host's rendered ansible_ssh_common_args (jump
// chain plus pinned options) into pinnedCommonArgsVar, the hostvar the
// forced extra var references.
func moveCommonArgs(doc map[string]any) {
	meta, _ := doc["_meta"].(map[string]any)
	hostvars, _ := meta["hostvars"].(map[string]any)
	for _, hv := range hostvars {
		vars, ok := hv.(map[string]any)
		if !ok {
			continue
		}
		if v, has := vars["ansible_ssh_common_args"]; has {
			vars[pinnedCommonArgsVar] = v
			delete(vars, "ansible_ssh_common_args")
		}
	}
}

// hostKeyAlias is the known_hosts name a pinned host's key is recorded
// under: the host's uuid, so the entry is independent of the address and
// port ansible dials (and of every other host sharing them).
func hostKeyAlias(hostID string) string {
	return "pulsix-host-" + hostID
}

// pinBastions returns the known_hosts lines of the route's pinned jump
// hosts (backlog 2.40): "pulsix-jump-<bastion-id> <key>", one authorized-key
// line per pinned bastion the connections actually dial. Every pinned
// bastion must come with its full key (a fingerprint alone cannot fill
// known_hosts) and one well-formed line — anything else fails the run
// closed. Pinned ids the claim does not connect to are ignored.
func pinBastions(connections map[string]string, bastionHostKeys, bastionKnownHosts map[string]string) (string, error) {
	used := map[string]bool{}
	for _, raw := range connections {
		var conn connection
		if err := json.Unmarshal([]byte(raw), &conn); err != nil {
			return "", fmt.Errorf("connection: %w", err)
		}
		for _, id := range conn.Bastions {
			used[id] = true
		}
	}
	ids := make([]string, 0, len(bastionHostKeys))
	for id := range bastionHostKeys {
		if used[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		key := strings.TrimSpace(bastionKnownHosts[id])
		if key == "" {
			return "", fmt.Errorf("bastion %s is pinned but the claim carries no pinned key: refusing to connect without host key checking", id)
		}
		if strings.ContainsAny(key, "\r\n") || len(strings.Fields(key)) < 2 {
			return "", fmt.Errorf("bastion %s: the pinned key is not one authorized-key line", id)
		}
		fmt.Fprintf(&b, "%s %s\n", bastionAlias(id), key)
	}
	return b.String(), nil
}

// pinHosts enforces the claim's host-key pins on ansible's own SSH
// connections (final review C2). For every host the claim pins (host_keys)
// it requires the pinned key (known_hosts) — one authorized-key line — and
//   - returns the known_hosts line "pulsix-host-<host-id> <key>",
//   - prepends "-o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-<id>"
//     to the host's ansible_ssh_common_args. ssh uses the first value it
//     obtains for an option, so prepending wins over anything later in the
//     same value and over the run's ssh_config defaults (accept-new).
//
// A pinned host whose key is missing or malformed fails the run closed —
// it must never be dialed without the check. Unpinned hosts are left to
// the ssh_config defaults: trust on first use within the run's wiped
// known_hosts. Pinned names the claim does not connect to are ignored.
func pinHosts(doc map[string]any, connections, hostKeys, knownHosts map[string]string) (string, error) {
	meta, _ := doc["_meta"].(map[string]any)
	hostvars, _ := meta["hostvars"].(map[string]any)
	names := make([]string, 0, len(hostKeys))
	for name := range hostKeys {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		rawConn, ok := connections[name]
		if !ok {
			continue
		}
		vars, ok := hostvars[name].(map[string]any)
		if !ok {
			continue
		}
		var conn connection
		if err := json.Unmarshal([]byte(rawConn), &conn); err != nil || conn.HostID == "" {
			return "", fmt.Errorf("pinned host %s: connection carries no host id", name)
		}
		key := strings.TrimSpace(knownHosts[name])
		if key == "" {
			return "", fmt.Errorf("host %s is pinned but the claim carries no pinned key: refusing to connect without host key checking", name)
		}
		if strings.ContainsAny(key, "\r\n") || len(strings.Fields(key)) < 2 {
			return "", fmt.Errorf("host %s: the pinned key is not one authorized-key line", name)
		}
		alias := hostKeyAlias(conn.HostID)
		strict := "-o StrictHostKeyChecking=yes -o HostKeyAlias=" + alias
		if args := varString(vars, "ansible_ssh_common_args"); args != "" {
			strict += " " + args
		}
		vars["ansible_ssh_common_args"] = strict
		fmt.Fprintf(&b, "%s %s\n", alias, key)
	}
	return b.String(), nil
}

// materializeVaultPassword writes one Ansible Vault password file per
// identity the job carries, returning them in label order, or nil when
// the job carries none — which is almost every run.
//
// Each passphrase arrives like every other revealed secret, in the job's
// own credentials map; a VaultIdentity is only the pointer saying which
// of them it is and what label Ansible must use to reach it. That keeps
// one channel for secrets and one redaction list covering all of them.
//
// A pointer naming a credential the claim did not reveal fails the run,
// naming the label and the credential id but never the password —
// carrying on would hand Ansible an encrypted file and no key, and its
// own "decryption failed" is exactly the confusing symptom this whole
// path exists to remove.
//
// A label is unique per parent (repository or template), not per run:
// internal/repos.ResolveVaultIdentities is supposed to have already
// merged the repository's set with the template's, template winning any
// collision, before the manager ever claims this job. Two identities
// reaching the runner under the same label is therefore a bug upstream,
// not something to paper over here — the second one would otherwise
// silently overwrite the first's file on disk.
//
// A label also reaches ansible.cfg verbatim, rendered into
// "label@path, label@path" inside an INI file (ansibleCfgText). The
// uuid-named password file already stops a label from reaching a
// filesystem path; this stops the same untrusted text from reaching that
// other place it lands. A newline would inject an arbitrary extra
// ansible.cfg key — host_key_checking = False would quietly undo the very
// host-key pinning this file exists to enforce — and a comma or "@" would
// forge an extra identity entry. This check is deliberate belt-and-braces
// alongside the write-time validation the API owns (internal/vaultlabel:
// non-empty, no leading/trailing whitespace, no "@", no comma, no Unicode
// control or format character); the runner is the last gate before a file
// that carries secrets and must not assume that validation ran.
func (r *Runner) materializeVaultPassword(job *workerpb.Job, env worker.RunEnv) ([]vaultFile, error) {
	identities := job.GetVaultIdentities()
	if len(identities) == 0 {
		return nil, nil
	}
	vaultDir := filepath.Join(env.WorkDir, vaultDirName)
	byLabel := make(map[string]string, len(identities)) // label -> credential id, duplicate check only
	files := make([]vaultFile, 0, len(identities))
	for _, vi := range identities {
		label, id := vi.GetLabel(), vi.GetCredentialId()
		if reason := invalidVaultLabel(label); reason != "" {
			return nil, fmt.Errorf("vault credential %s (label %q): %s", id, label, reason)
		}
		if prev, dup := byLabel[label]; dup {
			return nil, fmt.Errorf("vault identity label %q names both credential %s and %s: the claim must resolve this before naming it to the worker", label, prev, id)
		}
		byLabel[label] = id
		raw, ok := job.GetCredentials()[id]
		if !ok {
			return nil, fmt.Errorf("vault credential %s (label %q) named by the claim but not revealed in it", id, label)
		}
		var secret credentials.Secret
		if err := json.Unmarshal([]byte(raw), &secret); err != nil {
			return nil, fmt.Errorf("vault credential %s (label %q): %w", id, label, err)
		}
		if secret.Password == "" {
			return nil, fmt.Errorf("vault credential %s (label %q) carries no password", id, label)
		}
		path := filepath.Join(vaultDir, id)
		// A trailing newline is what a hand-written password file has and
		// what Ansible reads past; writing the bare string would be fine
		// too, but this matches what an operator would produce by hand.
		if err := writeSecretFile(path, secret.Password+"\n", 0o600); err != nil {
			return nil, err
		}
		files = append(files, vaultFile{Label: label, Path: path})
	}
	// Byte-stable output: without the sort, vault_identity_list's order
	// would follow the claim's wire order, which the manager's own
	// ResolveVaultIdentities already sorts — but the runner must not
	// depend on that upstream guarantee to produce a stable ansible.cfg.
	slices.SortFunc(files, func(a, b vaultFile) int { return strings.Compare(a.Label, b.Label) })
	return files, nil
}

// invalidVaultLabel reports why label cannot be written into ansible.cfg's
// vault_identity_list, or "" when it is fine. The rule itself lives in
// internal/vaultlabel, shared with internal/repos's write-time gate, so this
// last-chance check before rendering the config can never disagree with the
// one the API already applied — a disagreement would only surface as a run
// failing after a form already accepted the value. This gate has no length
// bound: internal/repos.MaxVaultLabelLength exists only to describe that
// service's OpenAPI contract, not because a long label is unsafe to render.
func invalidVaultLabel(label string) string {
	return vaultlabel.Invalid(label)
}
