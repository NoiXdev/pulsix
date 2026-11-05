// Ansible Vault was the one secret this product could hold nowhere. The
// worker generates its own ansible.cfg and points ANSIBLE_CONFIG at it,
// so a vault_password_file committed to a repository is ignored; nothing
// set ANSIBLE_VAULT_PASSWORD_FILE; and the runner never passed
// --vault-password-file. Meanwhile internal/ansiblevars already lets a
// variable's value carry Ansible's own vault marker through — so an
// encrypted blob reached Ansible and Ansible could not read it.
//
// v0.2e replaced the single vault_credential_id pointer with a repeated
// vault_identities field, because one repository can hold files encrypted
// under several different passwords (prod vs. staging, say). The manager
// hands the worker the already-resolved set
// (internal/repos.ResolveVaultIdentities); the runner's job is only to
// write one password file per identity and point ansible.cfg at all of
// them by label. secretJSON, used throughout, is inventory_test.go's
// helper in this same package.
package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

func TestVaultIdentitiesAreWrittenAndNamedInTheConfig(t *testing.T) {
	const (
		// prod's uuid sorts AFTER staging's — the opposite of their label
		// order — so a sort by credential id or by path would pass this
		// test identically to a sort by label. Only sorting by label
		// produces the line this test asserts.
		prodID    = "99999999-9999-9999-9999-999999999999"
		stagingID = "11111111-1111-1111-1111-111111111111"
	)
	job := &workerpb.Job{
		// Deliberately out of label order on the wire too: the runner must
		// sort, not trust the claim's order, so ansible.cfg is byte-stable
		// regardless of how the manager happened to enumerate the set.
		VaultIdentities: []*workerpb.VaultIdentity{
			{CredentialId: stagingID, Label: "staging"},
			{CredentialId: prodID, Label: "prod"},
		},
		Credentials: map[string]string{
			prodID:    secretJSON(t, credentials.Secret{Password: "prod-passphrase"}),
			stagingID: secretJSON(t, credentials.Secret{Password: "staging-passphrase"}),
		},
	}
	env := worker.RunEnv{WorkDir: t.TempDir()}

	if err := (&Runner{}).materializeRunnerConfig(job, env, ""); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}

	// N credentials produce N files, each 0600, named by credential id —
	// never by label, which is operator-entered text with no path-safety
	// guarantee (it could hold "../x" or a "/").
	for id, want := range map[string]string{prodID: "prod-passphrase", stagingID: "staging-passphrase"} {
		path := filepath.Join(env.WorkDir, vaultDirName, id)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read vault password file for %s: %v", id, err)
		}
		if got := strings.TrimRight(string(body), "\n"); got != want {
			t.Errorf("vault password file for %s = %q, want %q", id, got, want)
		}
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		} else if info.Mode().Perm() != 0o600 {
			t.Errorf("vault password file for %s mode = %v, want 0600", id, info.Mode().Perm())
		}
	}

	cfg, err := os.ReadFile(filepath.Join(env.WorkDir, ansibleCfgName))
	if err != nil {
		t.Fatalf("read ansible.cfg: %v", err)
	}
	prodPath := filepath.Join(env.WorkDir, vaultDirName, prodID)
	stagingPath := filepath.Join(env.WorkDir, vaultDirName, stagingID)
	wantLine := "vault_identity_list = prod@" + prodPath + ", staging@" + stagingPath
	if !strings.Contains(string(cfg), wantLine) {
		t.Errorf("ansible.cfg does not carry the expected vault_identity_list line:\nwant substring: %s\ngot:\n%s", wantLine, cfg)
	}
	if strings.Contains(string(cfg), "vault_password_file") {
		t.Errorf("ansible.cfg still names the single-password setting it must replace:\n%s", cfg)
	}
}

func TestNoVaultIdentitiesWritesNoFilesAndNoSetting(t *testing.T) {
	// The overwhelming majority of runs have no vault at all. They must
	// not get an empty password file or setting, which Ansible would try
	// to use and fail on more confusingly than having none configured.
	//
	// Review Focus 3 (old worker meets new manager): this is also the
	// shape a claim with nothing to decrypt takes. The runner never
	// inspects the playbook, so it cannot itself detect "this play needed
	// a vault and got none" — what this test pins is that the runner
	// does not paper over an empty set by fabricating a file or a
	// setting. If the playbook actually needs a vault, Ansible's own
	// "decryption failed, no vault secrets found" is what fails the run
	// loudly; the dangerous alternative this guards against is the
	// runner inventing something that makes that failure silent instead.
	env := worker.RunEnv{WorkDir: t.TempDir()}
	if err := (&Runner{}).materializeRunnerConfig(&workerpb.Job{}, env, ""); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.WorkDir, vaultDirName)); !os.IsNotExist(err) {
		t.Errorf("a job with no vault identities still got a vault directory (err=%v)", err)
	}
	cfg, err := os.ReadFile(filepath.Join(env.WorkDir, ansibleCfgName))
	if err != nil {
		t.Fatalf("read ansible.cfg: %v", err)
	}
	if strings.Contains(string(cfg), "vault_identity_list") || strings.Contains(string(cfg), "vault_password_file") {
		t.Errorf("ansible.cfg names a vault setting for a job without one:\n%s", cfg)
	}
}

func TestAVaultIdentityMissingFromTheMapFailsTheRun(t *testing.T) {
	// The pointer and the secret travel separately; a claim that names a
	// credential it did not reveal is a manager bug, and running anyway
	// would give Ansible's own "decryption failed" instead — the same
	// confusing symptom this whole change exists to remove.
	const missingID = "33333333-3333-3333-3333-333333333333"
	env := worker.RunEnv{WorkDir: t.TempDir()}
	job := &workerpb.Job{VaultIdentities: []*workerpb.VaultIdentity{{CredentialId: missingID, Label: "prod"}}}
	err := (&Runner{}).materializeRunnerConfig(job, env, "")
	if err == nil {
		t.Fatal("a named-but-absent vault credential must fail the run")
	}
	// Review Focus 5: the error must name the label and the credential id
	// so an operator can find the misconfigured identity, and must never
	// carry a password — there is none to carry here, but the assertion
	// on exact text is what would catch a future change that interpolates
	// the secret into the message.
	if !strings.Contains(err.Error(), "prod") {
		t.Errorf("error does not name the label %q: %v", "prod", err)
	}
	if !strings.Contains(err.Error(), missingID) {
		t.Errorf("error does not name the credential id %q: %v", missingID, err)
	}
}

func TestDuplicateVaultLabelFailsTheRun(t *testing.T) {
	// A label is unique per parent (repository or template), not per run
	// — repos.ResolveVaultIdentities is supposed to have already merged
	// the two sets before the worker ever sees them. Two identities
	// reaching the runner under the same label is a bug upstream, not
	// something the runner should paper over by letting the second file
	// silently overwrite the first's.
	const (
		firstID  = "44444444-4444-4444-4444-444444444444"
		secondID = "55555555-5555-5555-5555-555555555555"
	)
	job := &workerpb.Job{
		VaultIdentities: []*workerpb.VaultIdentity{
			{CredentialId: firstID, Label: "prod"},
			{CredentialId: secondID, Label: "prod"},
		},
		Credentials: map[string]string{
			firstID:  secretJSON(t, credentials.Secret{Password: "first-passphrase"}),
			secondID: secretJSON(t, credentials.Secret{Password: "second-passphrase"}),
		},
	}
	env := worker.RunEnv{WorkDir: t.TempDir()}
	err := (&Runner{}).materializeRunnerConfig(job, env, "")
	if err == nil {
		t.Fatal("two vault identities sharing a label must fail the run")
	}
	if !strings.Contains(err.Error(), "prod") {
		t.Errorf("error does not name the colliding label %q: %v", "prod", err)
	}
	if strings.Contains(err.Error(), "first-passphrase") || strings.Contains(err.Error(), "second-passphrase") {
		t.Errorf("error leaks a vault passphrase: %v", err)
	}
}

func TestUnsafeVaultLabelFailsTheRun(t *testing.T) {
	// The uuid-named password file closed the path-traversal door a label
	// could otherwise open; this closes the other one. A label lands
	// verbatim in ansible.cfg's vault_identity_list ("label@path, ..."),
	// so a newline forges an arbitrary extra key (host_key_checking =
	// False would quietly undo this very file's host-key pinning) and a
	// comma or "@" forges an extra identity entry. This is the runner's
	// own belt-and-braces check, independent of whatever validation the
	// API applies at write time.
	const credID = "66666666-6666-6666-6666-666666666666"
	cases := []string{
		"",
		"prod\nhost_key_checking = False",
		"prod\rhost_key_checking = False",
		"prod,staging",
		"prod@/etc/passwd",
		" prod",
		"prod ",
		// internal/vaultlabel's rule covers category Cc/Cf wholesale, not
		// only "\n\r,@": a byte-order mark (which JavaScript's trim()
		// strips but Go's strings.TrimSpace does not) and a
		// bidirectional-override character (which can make a label render
		// on an operator's screen as something other than what it is) must
		// fail here too, not only at the API's write-time gate.
		"\ufeffprod",
		"prod\u202e",
	}
	for _, label := range cases {
		t.Run(fmt.Sprintf("%q", label), func(t *testing.T) {
			job := &workerpb.Job{
				VaultIdentities: []*workerpb.VaultIdentity{{CredentialId: credID, Label: label}},
				Credentials:     map[string]string{credID: secretJSON(t, credentials.Secret{Password: "the-passphrase"})},
			}
			env := worker.RunEnv{WorkDir: t.TempDir()}
			err := (&Runner{}).materializeRunnerConfig(job, env, "")
			if err == nil {
				t.Fatalf("label %q must fail the run", label)
			}
			if !strings.Contains(err.Error(), credID) {
				t.Errorf("error does not name the credential id %q: %v", credID, err)
			}
			if strings.Contains(err.Error(), "the-passphrase") {
				t.Errorf("error leaks the vault passphrase: %v", err)
			}
		})
	}
}
