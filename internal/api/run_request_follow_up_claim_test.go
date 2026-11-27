package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
)

// approvedFollowUpQueued approves a run request whose template has the
// follow-up listmonk, with approvals starting follow-ups, runs the
// approved run to success on host A and lets the starter start listmonk.
// It returns listmonk's template and its queued job.
func approvedFollowUpQueued(t *testing.T, f runRequestFixture) (uuid.UUID, jobs.Job) {
	t.Helper()
	fu := f.withFollowUp(t, "listmonk")
	if resp := f.putTemplate(t, f.operatorC, fu, "listmonk", map[string]any{"extra_vars": map[string]any{"release": "1"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set listmonk's extra vars = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	f.setFollowUpsStart(t, true)
	created := f.request(t, map[string]any{"reason": "x"})
	d := f.detail(t, created.Request.ID)
	var p struct {
		FollowUps struct {
			Templates []struct {
				TemplateID string `json:"template_id"`
				Targets    []struct {
					ID string `json:"id"`
				} `json:"targets"`
				ExtraVars  map[string]any `json:"extra_vars"`
				VarsDigest string         `json:"vars_digest"`
			} `json:"templates"`
		} `json:"follow_ups"`
	}
	if err := json.Unmarshal(d.Picture, &p); err != nil {
		t.Fatal(err)
	}
	if n := p.FollowUps.Templates; len(n) != 1 || n[0].TemplateID != fu.String() || len(n[0].Targets) != 1 ||
		n[0].Targets[0].ID != f.hostA.String() || n[0].ExtraVars["release"] != "1" || n[0].VarsDigest == "" {
		t.Fatalf("the picture's follow-ups = %s, want listmonk bound to host A", d.Picture)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1")
	if err != nil || c == nil {
		t.Fatalf("claim of the approved run = %v, %v", c, err)
	}
	if err := f.s.Queue.Finish(context.Background(), uuid.MustParse(c.Job.GetId()), jobs.StatusSucceeded,
		map[string]any{"ok_hosts": []string{f.hostA.String()}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChainStarter.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	queued, _, err := f.s.Deps.Jobs.List(f.scoped, f.orgID, jobs.Filter{TemplateID: &fu}, "", 5)
	if err != nil || len(queued) != 1 || queued[0].Status != jobs.StatusQueued {
		t.Fatalf("listmonk's jobs = %+v, %v; want one queued", queued, err)
	}
	return fu, queued[0]
}

// TestApprovedFollowUpClaimIsBoundToTheApproval is backlog 2.143 with the
// owner's decision of 2026-11-27: the follow-up an approval starts runs
// only with the hosts and vars the approval showed. Unchanged, its job is
// claimed; with its extra vars changed after the approval, its claim is
// refused like a changed run's.
func TestApprovedFollowUpClaimIsBoundToTheApproval(t *testing.T) {
	t.Run("unchanged it runs", func(t *testing.T) {
		f := newRunRequestFixture(t, "rr-fu-bound")
		_, job := approvedFollowUpQueued(t, f)
		c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1")
		if err != nil || c == nil || c.Job.GetId() != job.ID.String() {
			t.Fatalf("claim of the follow-up = %v, %v; want %s claimed", c, err, job.ID)
		}
	})
	t.Run("changed vars refuse it", func(t *testing.T) {
		f := newRunRequestFixture(t, "rr-fu-changed")
		fu, job := approvedFollowUpQueued(t, f)
		if resp := f.putTemplate(t, f.operatorC, fu, "listmonk", map[string]any{"extra_vars": map[string]any{"release": "2"}}); resp.StatusCode != http.StatusOK {
			t.Fatalf("change listmonk's extra vars = %d %s", resp.StatusCode, mustReadBody(t, resp))
		}
		if c, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1"); err != nil || c != nil {
			t.Fatalf("claim of the changed follow-up = %v, %v; want refused", c, err)
		}
		refused, err := f.s.Deps.Jobs.Get(f.scoped, f.orgID, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if refused.Status != jobs.StatusError || !strings.HasPrefix(refused.Error, "refused at claim: what the run uses changed after it was approved") ||
			refused.WithdrawnAtClaim() {
			t.Fatalf("listmonk's job = %s %q, want refused because it changed", refused.Status, refused.Error)
		}
	})
}
