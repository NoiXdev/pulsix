package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// Ansible Vault was the one secret this product could hold nowhere. The
// worker generates its own ansible.cfg and points ANSIBLE_CONFIG at it,
// so a vault_password_file committed to a repository is ignored; nothing
// set ANSIBLE_VAULT_PASSWORD_FILE; and the runner never passed
// --vault-password-file. Meanwhile internal/ansiblevars already lets a
// variable's value carry Ansible's own vault marker through — so an
// encrypted blob reached Ansible and Ansible could not read it.
func TestVaultPasswordIsWrittenAndNamedInTheConfig(t *testing.T) {
	secret, err := json.Marshal(credentials.Secret{Password: "the-vault-passphrase"})
	if err != nil {
		t.Fatalf("encode secret: %v", err)
	}
	const credID = "11111111-1111-1111-1111-111111111111"
	job := &workerpb.Job{
		VaultCredentialId: credID,
		Credentials:       map[string]string{credID: string(secret)},
	}
	env := worker.RunEnv{WorkDir: t.TempDir()}

	if err := (&Runner{}).materializeRunnerConfig(job, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}

	pwPath := filepath.Join(env.WorkDir, vaultPasswordName)
	body, err := os.ReadFile(pwPath)
	if err != nil {
		t.Fatalf("read the vault password file: %v", err)
	}
	// Ansible reads the whole first line; a trailing newline is fine and
	// a trailing space is not, so the file holds the passphrase exactly.
	if got := strings.TrimRight(string(body), "\n"); got != "the-vault-passphrase" {
		t.Errorf("vault password file = %q, want the passphrase", got)
	}
	if info, err := os.Stat(pwPath); err != nil {
		t.Fatalf("stat: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("vault password file mode = %v, want 0600", info.Mode().Perm())
	}

	cfg, err := os.ReadFile(filepath.Join(env.WorkDir, ansibleCfgName))
	if err != nil {
		t.Fatalf("read ansible.cfg: %v", err)
	}
	if !strings.Contains(string(cfg), "vault_password_file = "+pwPath) {
		t.Errorf("ansible.cfg does not name the vault password file:\n%s", cfg)
	}
}

func TestNoVaultCredentialWritesNoFileAndNoSetting(t *testing.T) {
	// The overwhelming majority of runs have no vault at all. They must
	// not get an empty password file, which Ansible would try to use and
	// fail on more confusingly than having none.
	env := worker.RunEnv{WorkDir: t.TempDir()}
	if err := (&Runner{}).materializeRunnerConfig(&workerpb.Job{}, env); err != nil {
		t.Fatalf("materializeRunnerConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.WorkDir, vaultPasswordName)); !os.IsNotExist(err) {
		t.Errorf("a job with no vault credential still got a password file (err=%v)", err)
	}
	cfg, err := os.ReadFile(filepath.Join(env.WorkDir, ansibleCfgName))
	if err != nil {
		t.Fatalf("read ansible.cfg: %v", err)
	}
	if strings.Contains(string(cfg), "vault_password_file") {
		t.Errorf("ansible.cfg names a vault password file for a job without one:\n%s", cfg)
	}
}

func TestAVaultCredentialMissingFromTheMapFailsTheRun(t *testing.T) {
	// The pointer and the secret travel separately; a claim that names a
	// credential it did not reveal is a manager bug, and running anyway
	// would give Ansible's own "decryption failed" instead — the same
	// confusing symptom this whole change exists to remove.
	env := worker.RunEnv{WorkDir: t.TempDir()}
	job := &workerpb.Job{VaultCredentialId: "22222222-2222-2222-2222-222222222222"}
	if err := (&Runner{}).materializeRunnerConfig(job, env); err == nil {
		t.Fatal("a named-but-absent vault credential must fail the run")
	}
}
