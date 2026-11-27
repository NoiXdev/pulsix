package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestRunRequestReapprovalRefusesAHostThatJoinedACoveredZone is backlog
// 2.142 with the owner's decision of 2026-11-27: the first approval
// enqueues zone A's job and fails before zone B's; then a host joins
// zone A. Approving again would replay zone A's job, which does not hold
// the new host, so the host would never run: the re-approval is refused
// with a 409 that names the zone and the host, nothing is enqueued (not
// even zone B's job) and the request stays pending.
func TestRunRequestReapprovalRefusesAHostThatJoinedACoveredZone(t *testing.T) {
	f := newRunRequestFixture(t, "rr-covered-zone")
	f.targetBothHosts(t)
	created := f.request(t, map[string]any{"reason": "x"})
	id := created.Request.ID

	lift := refuseJobInserts(t, f.zoneB)
	resp := f.approve(t, f.adminC, id, f.detail(t, id).Fingerprint)
	if body := mustReadBody(t, resp); resp.StatusCode == http.StatusOK {
		t.Fatalf("approve with zone B's insert failing succeeded: %s", body)
	}
	lift()
	if first := f.groupJobs(t, id); len(first) != 1 || first[0].ZoneID != f.zoneA {
		t.Fatalf("jobs after the interrupted enqueue = %+v, want zone A's only", first)
	}

	joined, err := f.s.Deps.Hosts.CreateHost(f.scoped, f.orgID, hosts.HostInput{Name: "web-1b", Address: "10.0.0.2", ZoneID: f.zoneA, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	cred := f.hostCredential(t)
	if _, err := f.s.Deps.SSH.SetHostSSH(f.scoped, f.orgID, joined.ID, sshconf.HostSSHInput{CredentialID: &cred, Port: 22, BastionMode: sshconf.ModeInherit}); err != nil {
		t.Fatal(err)
	}
	updateTemplate(t, f, func(in *templates.UpdateInput) {
		in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB, joined.ID}}
	})

	resp = f.approve(t, f.adminC, id, f.detail(t, id).Fingerprint)
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-approve = %d %s, want 409", resp.StatusCode, body)
	}
	var problem struct {
		Detail string `json:"detail"`
		Code   string `json:"code"`
		Zones  []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Hosts []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"hosts"`
		} `json:"zones"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "hosts_joined_covered_zone" || len(problem.Zones) != 1 || problem.Zones[0].ID != f.zoneA.String() ||
		len(problem.Zones[0].Hosts) != 1 || problem.Zones[0].Hosts[0].ID != joined.ID.String() || problem.Zones[0].Hosts[0].Name != "web-1b" {
		t.Fatalf("problem = %s", body)
	}
	if !strings.Contains(problem.Detail, "web-1b") || !strings.Contains(problem.Detail, problem.Zones[0].Name) {
		t.Fatalf("detail = %q, want the zone and the host named", problem.Detail)
	}
	if all := f.groupJobs(t, id); len(all) != 1 {
		t.Fatalf("jobs after the refused re-approval = %d, want zone A's first job only", len(all))
	}
	if st := f.detail(t, id).Request.Status; st != "pending" {
		t.Fatalf("status after the refused re-approval = %s, want pending", st)
	}
}
