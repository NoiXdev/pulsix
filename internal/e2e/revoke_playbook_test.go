// The system playbooks against the real ansible-core of the worker image
// (backlogs 7.45 and 7.46).
package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker/playbooks"
)

const (
	playbookOldKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDOLDO old"
	playbookKeepKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKEEPKEEPKEEPKEEPKEEPKEEPKEEPKEEPKEEPKEEPKEE keep"
	playbookNewKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEW new"
)

// runSystemPlaybook runs one embedded system playbook as root on the
// worker image's own ansible-core, against local accounts that setup
// creates, with extraVars as its extra vars; it returns the output of the
// whole script, which ends with report.
func runSystemPlaybook(t *testing.T, name, setup, extraVars, report string) string {
	t.Helper()
	image := workerImageOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	book, err := playbooks.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "play.yml"), book, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vars.json"), []byte(extraVars), 0o644); err != nil {
		t.Fatal(err)
	}
	// The image has no sudo and no visudo: the run is root already, and the
	// sudoers rule's syntax check is the target host's business.
	script := "set -eu\n" + setup + `
printf '#!/bin/sh\nexit 0\n' > /usr/local/bin/visudo && chmod 755 /usr/local/bin/visudo
cd /w
ANSIBLE_LOCALHOST_WARNING=0 ansible-playbook -i localhost, -c local play.yml -e ansible_become=false -e @vars.json
` + report
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--user", "0",
		"-e", "OLD_KEY="+playbookOldKey, "-e", "KEEP_KEY="+playbookKeepKey,
		"-v", dir+":/w:ro", "--entrypoint", "sh", image, "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
	return string(out)
}

// keysOf is the part of out the report printed for account.
func keysOf(out, account string) string {
	_, rest, _ := strings.Cut(out, "== "+account+"\n")
	keys, _, _ := strings.Cut(rest, "== ")
	return keys
}

const twoAccountsWithOldAndKeep = `
for u in deploy ansible; do
  useradd -m "$u"
  mkdir -p "/home/$u/.ssh"
  printf '%s\n%s\n' "$OLD_KEY" "$KEEP_KEY" > "/home/$u/.ssh/authorized_keys"
  chown -R "$u" "/home/$u/.ssh"
done`

const reportKeys = `for u in deploy ansible; do echo "== $u"; stat -c '%U %a' "/home/$u/.ssh" "/home/$u/.ssh/authorized_keys" 2>/dev/null || true; cat "/home/$u/.ssh/authorized_keys" 2>/dev/null || true; done
echo "== end"`

// TestRevokePlaybookClearsTheOtherAccountsToo is backlog 7.45. A rotation
// revokes the previous key from the account runs log in as (7.33), but
// bootstrap installs a credential's key for the credential's own account,
// which can be a different one. The revocation therefore also removes the
// key from each account in pulsix_also_users that exists on the host, and
// an account that does not exist is no failure.
func TestRevokePlaybookClearsTheOtherAccountsToo(t *testing.T) {
	out := runSystemPlaybook(t, playbooks.NameRevokeKey, twoAccountsWithOldAndKeep,
		`{"pulsix_user": "deploy", "pulsix_old_public_key": "`+playbookOldKey+`", "pulsix_also_users": ["ansible", "ghost"]}`,
		reportKeys)
	for _, account := range []string{"deploy", "ansible"} {
		keys := keysOf(out, account)
		if strings.Contains(keys, playbookOldKey) {
			t.Errorf("the previous key is still in %s's authorized_keys:\n%s", account, out)
		}
		if !strings.Contains(keys, playbookKeepKey) {
			t.Errorf("another key was removed from %s's authorized_keys:\n%s", account, out)
		}
	}
}

// TestRotatePlaybookInstallsTheNewKey is backlog 7.46: the system
// playbooks named ansible.builtin.authorized_key, which ansible-core does
// not ship (it lives in the ansible.posix collection, which the image does
// not carry), so none of them could even be loaded. The rotation's install
// must add the new key and leave the others.
func TestRotatePlaybookInstallsTheNewKey(t *testing.T) {
	out := runSystemPlaybook(t, playbooks.NameRotateKey, twoAccountsWithOldAndKeep,
		`{"pulsix_user": "deploy", "pulsix_public_key": "`+playbookNewKey+`"}`, reportKeys)
	keys := keysOf(out, "deploy")
	for _, want := range []string{playbookNewKey, playbookOldKey, playbookKeepKey} {
		if !strings.Contains(keys, want) {
			t.Errorf("deploy's authorized_keys lacks %q after the rotation's install:\n%s", want, out)
		}
	}
	if strings.Contains(keysOf(out, "ansible"), playbookNewKey) {
		t.Errorf("the new key went to another account:\n%s", out)
	}
}

// TestBootstrapPlaybookCreatesTheAccountWithTheKey is backlog 7.46 for the
// first install: a fresh account, its key in place with the modes sshd
// insists on, and the sudo rule.
func TestBootstrapPlaybookCreatesTheAccountWithTheKey(t *testing.T) {
	// A host with sudo has the drop-in directory; the image does not.
	out := runSystemPlaybook(t, playbooks.NameBootstrap, "mkdir -p /etc/sudoers.d",
		`{"pulsix_user": "ansible", "pulsix_public_key": "`+playbookNewKey+`"}`,
		reportKeys+`
echo "== sudoers"; cat /etc/sudoers.d/90-pulsix-ansible; stat -c '%a' /etc/sudoers.d/90-pulsix-ansible`)
	keys := keysOf(out, "ansible")
	if !strings.Contains(keys, playbookNewKey) {
		t.Errorf("the bootstrapped account has no key:\n%s", out)
	}
	if !strings.Contains(keys, "ansible 700") || !strings.Contains(keys, "ansible 600") {
		t.Errorf("the account's .ssh is not owner-only:\n%s", out)
	}
	if !strings.Contains(out, "ansible ALL=(ALL) NOPASSWD:ALL") || !strings.Contains(out, "\n440\n") {
		t.Errorf("no sudo rule for the account:\n%s", out)
	}
}
