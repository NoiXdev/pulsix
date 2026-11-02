package jobs_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// TestRotationActsOnTheAccountRunsLogInAs is backlog 7.33. Runs log in as
// the host's own SSH username when it has one, and only otherwise as the
// credential's; the rotation group used the opposite order. On a host
// whose SSH username is "deploy" and whose credential says "ansible", the
// new key went to ansible, was verified as ansible, and the old one was
// removed from ansible — while deploy, the account every run uses, kept
// the old key and never got the new one. The install, the verification
// and the revocation must all use the account the runs log in as.
func TestRotationActsOnTheAccountRunsLogInAs(t *testing.T) {
	f := newRotationFixture(t, "jobs-rotation-account")
	if _, err := f.w.sshSvc.SetHostSSH(f.w.scoped, f.w.org, f.hostID, sshconf.HostSSHInput{
		Username: "deploy", CredentialID: &f.cred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := f.w.credsSvc.Rotate(f.w.scoped, f.w.org, f.cred.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	type accountPayload struct {
		Host struct {
			Username string `json:"username"`
		} `json:"host"`
		Username   string `json:"username"`
		PulsixUser string `json:"pulsix_user"`
	}
	check := func(kind jobs.Kind) {
		t.Helper()
		c := f.claim(t)
		if c == nil || c.Job.GetKind() != string(kind) {
			t.Fatalf("claim = %v, want the %s", c, kind)
		}
		var p accountPayload
		if err := json.Unmarshal([]byte(c.Job.GetKindPayload()), &p); err != nil {
			t.Fatal(err)
		}
		// A connection_test carries the account in host.username alone.
		got := p.Username
		if got == "" {
			got = p.Host.Username
		}
		if got != "deploy" || (p.PulsixUser != "" && p.PulsixUser != "deploy") {
			t.Fatalf("%s acts on login %q, pulsix_user %q (host %q); want deploy throughout", kind, p.Username, p.PulsixUser, p.Host.Username)
		}
		if err := f.w.queue.Finish(ctx, uuid.MustParse(c.Job.GetId()), jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	check(jobs.KindRotateKey)
	check(jobs.KindConnectionTest)
	if revokes := f.groupJobs(t, result.GroupID)[jobs.KindRevokeKey]; len(revokes) != 1 {
		t.Fatalf("revocations = %+v, want one", revokes)
	}
	check(jobs.KindRevokeKey)
}
