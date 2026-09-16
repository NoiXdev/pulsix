// The claim scope against the real store and the real renderer (final
// review C1): a whole-org template spanning two zones splits into one job
// per zone, and each zone's claim renders only that zone's hosts; a
// retry-failed run renders only the host that failed; a host whose key
// changed between enqueue and claim is excluded at claim time.
package jobs_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// renderedHosts reads the host names a claim's inventory document carries.
func renderedHosts(t *testing.T, doc string) []string {
	t.Helper()
	var inv struct {
		Meta struct {
			Hostvars map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(doc), &inv); err != nil {
		t.Fatalf("inventory document: %v", err)
	}
	var names []string
	for name := range inv.Meta.Hostvars {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestClaimRendersPerZoneAndRetryHostsAgainstTheRealRenderer(t *testing.T) {
	w := newRotationStoreWiring(t, "jobs-claim-scope-real")
	zoneA := w.zone(t)
	zoneB, err := w.hostsSvc.CreateZone(w.scoped, w.org, "b", "Zone B")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := w.credsSvc.Create(w.scoped, w.org, credentials.CreateInput{
		Name: "login", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]uuid.UUID{}
	for _, h := range []struct {
		name string
		zone uuid.UUID
	}{{"a-01", zoneA}, {"a-02", zoneA}, {"b-01", zoneB.ID}} {
		created, err := w.hostsSvc.CreateHost(w.scoped, w.org, hosts.HostInput{Name: h.name, Address: "10.0.0.1", ZoneID: h.zone, Managed: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.sshSvc.SetHostSSH(w.scoped, w.org, created.ID, sshconf.HostSSHInput{
			CredentialID: &cred.ID, Port: 22, BastionMode: sshconf.ModeInherit,
		}); err != nil {
			t.Fatal(err)
		}
		ids[h.name] = created.ID
	}

	playbookID, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := w.d.Exec(w.scoped,
		`INSERT INTO playbooks (id, org_id, inline, source) VALUES ($1, $2, $3, 'editor')`,
		playbookID, w.org, "- hosts: all\n  tasks: []"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Exec(w.scoped,
		`INSERT INTO job_templates (id, org_id, name, playbook_id, scope) VALUES ($1, $2, $3, $4, '{}')`,
		templateID, w.org, "whole-org", playbookID); err != nil {
		t.Fatal(err)
	}

	created, err := w.svc.EnqueueRun(w.scoped, w.org, templateID, nil)
	if err != nil {
		t.Fatalf("EnqueueRun: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("a two-zone scope must split into two jobs, got %d", len(created))
	}

	// Each zone's claim renders exactly that zone's hosts.
	want := map[uuid.UUID][]string{zoneA: {"a-01", "a-02"}, zoneB.ID: {"b-01"}}
	var zoneAJob uuid.UUID
	for _, zone := range []uuid.UUID{zoneA, zoneB.ID} {
		claim, err := w.queue.Claim(ctx, w.org, []uuid.UUID{zone}, "v1")
		if err != nil || claim == nil {
			t.Fatalf("claim zone %s = %v, %v", zone, claim, err)
		}
		if got := renderedHosts(t, claim.Job.GetInventoryDoc()); !slices.Equal(got, want[zone]) {
			t.Fatalf("zone %s claim rendered %v, want %v", zone, got, want[zone])
		}
		if len(claim.Job.GetConnections()) != len(want[zone]) {
			t.Fatalf("zone %s claim connections = %v", zone, claim.Job.GetConnections())
		}
		if zone == zoneA {
			zoneAJob = uuid.MustParse(claim.Job.GetId())
		}
	}

	// Zone A's run failed on a-02: the retry claims render only a-02.
	if err := w.queue.Finish(ctx, zoneAJob, jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts: []any{ids["a-02"].String()},
	}, ""); err != nil {
		t.Fatal(err)
	}
	retry, err := w.svc.RetryFailed(w.scoped, w.org, zoneAJob, nil)
	if err != nil || len(retry) != 1 {
		t.Fatalf("RetryFailed = %v, %v", retry, err)
	}
	claim, err := w.queue.Claim(ctx, w.org, []uuid.UUID{zoneA}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("retry claim = %v, %v", claim, err)
	}
	if got := renderedHosts(t, claim.Job.GetInventoryDoc()); !slices.Equal(got, []string{"a-02"}) {
		t.Fatalf("retry claim rendered %v, want only the failed host a-02", got)
	}

	// A key that changes between enqueue and claim excludes the host at
	// claim time: enqueue both zone-A hosts, then a test reports a-01's
	// key as changed before the worker claims.
	if _, err := w.svc.EnqueueRun(w.scoped, w.org, templateID, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.hostsSvc.RecordConnectionTest(w.scoped, w.org, ids["a-01"], time.Now(), false,
		"SHA256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC", "", hosts.KeyStateChanged, nil); err != nil {
		t.Fatal(err)
	}
	claim, err = w.queue.Claim(ctx, w.org, []uuid.UUID{zoneA}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("post-change claim = %v, %v", claim, err)
	}
	if got := renderedHosts(t, claim.Job.GetInventoryDoc()); !slices.Equal(got, []string{"a-02"}) {
		t.Fatalf("post-change claim rendered %v, want a-01 excluded", got)
	}
}
