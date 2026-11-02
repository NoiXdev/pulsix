// The revoke-key executor's tests (final review I1): the revocation logs in
// with the credential's CURRENT key (the new one) and removes the previous
// public key, carried as its own extra var — nothing else.
package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
)

func revokeClaim(t *testing.T, host map[string]any, username, login, oldPub string) string {
	t.Helper()
	return sysPayload(t, map[string]any{
		"host": host, "username": username, "login_credential": login,
		"old_public_key": oldPub, "pulsix_user": username,
	})
}

func TestRevokeExecutorRemovesThePreviousKeyWithTheNewLogin(t *testing.T) {
	cap := &captureExecutor{status: StatusSucceeded}
	host := map[string]any{"id": "host-uuid", "name": "web1", "address": "192.0.2.10", "port": 22}
	login := secretStr(t, credentials.Secret{PrivateKey: "new-key-material"})
	job := sysJob(t, "revoke_key", revokeClaim(t, host, "ansible", login, "ssh-ed25519 AAAA oldpub"),
		map[string]string{"web1": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	job.KnownHosts = map[string]string{"web1": "ssh-ed25519 AAAA hostkey"}
	job.Redact = []string{"new-key-material"}
	status, _, err := NewRevokeExecutor(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}
	got := cap.got
	if got.GetKind() != "revoke_key" {
		t.Fatalf("kind = %q", got.GetKind())
	}
	if got.GetCredentials()["pulsix-login"] != login {
		t.Fatalf("login = %q, want the credential's current (new) key", got.GetCredentials()["pulsix-login"])
	}
	if got.GetExtraVars()["pulsix_old_public_key"] != `"ssh-ed25519 AAAA oldpub"` || got.GetExtraVars()["pulsix_user"] != `"ansible"` {
		t.Fatalf("extra vars = %v", got.GetExtraVars())
	}
	if _, has := got.GetExtraVars()["pulsix_public_key"]; has {
		t.Fatal("a revocation must never carry a key to install")
	}
	if len(got.GetRedact()) != 1 || got.GetRedact()[0] != "new-key-material" {
		t.Fatalf("the redact list did not ride into the runner claim: %v", got.GetRedact())
	}
	if got.GetKnownHosts()["web1"] != "ssh-ed25519 AAAA hostkey" {
		t.Fatalf("the pin did not ride into the runner claim: %v", got.GetKnownHosts())
	}
}

func TestRevokeExecutorBrokenPayloadsFailClosed(t *testing.T) {
	host := map[string]any{"id": "u1", "name": "web1", "address": "192.0.2.10", "port": 22}
	for name, payload := range map[string]string{
		"not json":          "{",
		"no old key":        revokeClaim(t, host, "ansible", `{"private_key":"k"}`, ""),
		"no login":          revokeClaim(t, host, "ansible", "", "ssh-ed25519 AAAA old"),
		"no target address": revokeClaim(t, map[string]any{"name": "web1"}, "ansible", `{"private_key":"k"}`, "ssh-ed25519 AAAA old"),
	} {
		t.Run(name, func(t *testing.T) {
			cap := &captureExecutor{}
			status, _, err := NewRevokeExecutor(cap).Run(context.Background(), sysJob(t, "revoke_key", payload, nil), RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
			if err == nil || status != StatusError || cap.got != nil {
				t.Fatalf("Run = %q, %v, ran %v", status, err, cap.got != nil)
			}
		})
	}
	if _, _, err := NewRevokeExecutor(&captureExecutor{}).Run(context.Background(), sysJob(t, "rotate_key", "{}", nil), RunEnv{}, (&evCollector{}).sink); err == nil || !strings.Contains(err.Error(), "revoke") {
		t.Fatalf("a foreign kind must be refused: %v", err)
	}
}

// TestRevokeExecutorPassesTheOtherAccounts is backlog 7.45: the accounts
// besides the login one that the previous key may sit in ride into the
// playbook as pulsix_also_users, a JSON list; without any, the list is
// empty rather than missing.
func TestRevokeExecutorPassesTheOtherAccounts(t *testing.T) {
	host := map[string]any{"id": "host-uuid", "name": "web1", "address": "192.0.2.10", "port": 22}
	login := secretStr(t, credentials.Secret{PrivateKey: "new-key-material"})
	for _, c := range []struct {
		also []string
		want string
	}{
		{[]string{"ansible"}, `["ansible"]`},
		{nil, `[]`},
	} {
		cap := &captureExecutor{status: StatusSucceeded}
		payload := sysPayload(t, map[string]any{
			"host": host, "username": "deploy", "login_credential": login,
			"old_public_key": "ssh-ed25519 AAAA oldpub", "pulsix_user": "deploy", "also_users": c.also,
		})
		job := sysJob(t, "revoke_key", payload, map[string]string{"web1": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
		job.KnownHosts = map[string]string{"web1": "ssh-ed25519 AAAA hostkey"}
		if status, _, err := NewRevokeExecutor(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink); err != nil || status != StatusSucceeded {
			t.Fatalf("Run = %q, %v", status, err)
		}
		if got := cap.got.GetExtraVars()["pulsix_also_users"]; got != c.want {
			t.Errorf("also_users %v: pulsix_also_users = %q, want %q", c.also, got, c.want)
		}
	}
}
