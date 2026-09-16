// The rotate-key executor's tests: the pending new key shapes the run (the
// old credential authenticates, the new public key rides as the extra var)
// and the pin rides into the runner-shaped claim like the bootstrap's. The full delegation into
// the ansible-runner machinery runs in system_e2e_test.go.
package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
)

// rotateClaim is the claim payload shape internal/jobs ships.
func rotateClaim(t *testing.T, host map[string]any, username, oldSecret, newPub, newPriv, pulsixUser string) string {
	t.Helper()
	return sysPayload(t, map[string]any{
		"host": host, "username": username,
		"old_credential": oldSecret, "new_public_key": newPub,
		"new_private_key": newPriv, "new_fingerprint": "SHA256:pending", "new_key_type": "ed25519",
		"pulsix_user": pulsixUser,
	})
}

func TestRotateExecutorRunsThePlaybookWithTheOldLogin(t *testing.T) {
	cap := &captureExecutor{status: StatusSucceeded}
	host := map[string]any{"id": "host-uuid", "name": "web1", "address": "192.0.2.10", "port": 2222}
	old := secretStr(t, credentials.Secret{PrivateKey: "old-key-material"})
	status, _, err := NewRotateExecutor(cap).Run(context.Background(),
		sysJob(t, "rotate_key", rotateClaim(t, host, "ansible", old, "ssh-ed25519 AAAA newpub", "", "ansible"), nil),
		RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err != nil || status != StatusSucceeded {
		t.Fatalf("Run = %q, %v", status, err)
	}

	got := cap.got
	if got.GetKind() != "rotate_key" {
		t.Fatalf("kind = %q (the bookends must stay truthful to what ran)", got.GetKind())
	}
	if login := got.GetCredentials()["pulsix-login"]; login != old {
		t.Fatalf("login credential = %q, want the OLD credential", login)
	}
	var doc struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(got.GetInventoryDoc()), &doc); err != nil {
		t.Fatal(err)
	}
	vars := doc.Meta.Hostvars["web1"]
	if vars["ansible_port"] != float64(2222) || vars["ansible_user"] != "ansible" {
		t.Fatalf("hostvars = %v", vars)
	}
	if got.GetExtraVars()["pulsix_public_key"] != `"ssh-ed25519 AAAA newpub"` {
		t.Fatalf("extra vars = %v, want the NEW public key", got.GetExtraVars())
	}
	// The pending new private key stays out of the run's claim material —
	// only the manager's finalize step consumes it.
	for _, v := range got.GetExtraVars() {
		if strings.Contains(v, "new-key-material") {
			t.Errorf("the pending private key leaked into the run: %s", v)
		}
	}
	if strings.Contains(got.GetInventoryDoc(), "new-key-material") {
		t.Error("the pending private key leaked into the inventory")
	}
}

// TestRotateExecutorHandsThePinToTheRunner: like the bootstrap, no direct
// dial — the pin rides into the runner-shaped claim and the runner enforces
// it on ansible's own connection through the jump chain.
func TestRotateExecutorHandsThePinToTheRunner(t *testing.T) {
	cap := &captureExecutor{status: StatusSucceeded}
	job := sysJob(t, "rotate_key", rotateClaim(t, map[string]any{
		"id": "u1", "name": "web1", "address": "192.0.2.9", "port": 22,
	}, "ansible", secretStr(t, credentials.Secret{Password: "old"}), "pub", "{}", "ansible"),
		map[string]string{"web1": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	job.KnownHosts = map[string]string{"web1": "ssh-ed25519 AAAA pinned"}
	status, _, err := NewRotateExecutor(cap).Run(context.Background(), job, RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
	if err != nil || status != StatusSucceeded || cap.got == nil {
		t.Fatalf("pinned rotation = %q, %v, ran %v; want it handed to the runner without a preflight dial", status, err, cap.got != nil)
	}
	if cap.got.GetHostKeys()["web1"] == "" || cap.got.GetKnownHosts()["web1"] != "ssh-ed25519 AAAA pinned" {
		t.Fatalf("runner claim pins = %v / %v", cap.got.GetHostKeys(), cap.got.GetKnownHosts())
	}
}

func TestRotateExecutorBrokenPayloadsFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"not json", `{oops`, "kind payload"},
		{"no host", `{"old_credential":"{}"}`, "no target"},
		{"no old credential", `{"host":{"name":"h","address":"a"}}`, "no target"},
		{"no new public key", `{"host":{"name":"h","address":"a"},"old_credential":"{}"}`, "no target"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, _, err := NewRotateExecutor(&captureExecutor{}).Run(context.Background(),
				sysJob(t, "rotate_key", c.payload, nil), RunEnv{WorkDir: t.TempDir()}, (&evCollector{}).sink)
			if err == nil || status != StatusError {
				t.Fatalf("status = %q err = %v", status, err)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %v, want it to name %q", err, c.wantErr)
			}
		})
	}
}

func TestRotateExecutorUnknownKindIsAnError(t *testing.T) {
	status, _, err := NewRotateExecutor(&captureExecutor{}).Run(context.Background(),
		sysJob(t, "bootstrap", `{}`, nil), RunEnv{}, (&evCollector{}).sink)
	if err == nil || status != StatusError {
		t.Fatalf("status = %q err = %v", status, err)
	}
	if !strings.Contains(err.Error(), "rotate") {
		t.Errorf("error names the kind: %v", err)
	}
}
