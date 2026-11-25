package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// fakeAgents is the hosts that run an agent.
type fakeAgents struct {
	with  map[uuid.UUID]bool
	err   error
	calls int
}

func (f *fakeAgents) HostsWithAgent(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.calls++
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if f.with[id] {
			out[id] = true
		}
	}
	return out, f.err
}

// enrolling switches agent enrollment on for template X; web-1 runs an
// agent.
func enrolling(w *world) {
	tmpl := w.tmpls.fakeTemplates[w.x]
	tmpl.AgentEnrollment = true
	w.tmpls.fakeTemplates[w.x] = tmpl
	w.agents.with[w.h1] = true
}

func enrollOf(t *testing.T, p runrequests.Picture) runrequests.PictureAgentEnrollment {
	t.Helper()
	if p.AgentEnrollment == nil {
		t.Fatal("the picture has no agent enrollment")
	}
	return *p.AgentEnrollment
}

// TestPictureEnrolsTheTargetsWithoutAnAgent (spec 2026-11-25 B): a request
// for a template with agent enrollment lists the targets that have no
// agent now, by name with their ids; a template without the flag has no
// section, and its picture asks nothing about agents.
func TestPictureEnrolsTheTargetsWithoutAnAgent(t *testing.T) {
	w := newWorld()
	if p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}); p.AgentEnrollment != nil || w.agents.calls != 0 {
		t.Fatalf("without the flag: %+v after %d agent lookups", p.AgentEnrollment, w.agents.calls)
	}
	enrolling(w)
	e := enrollOf(t, w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}))
	if !slices.Equal(e.Hosts, []string{"web-2", "web-3"}) || !slices.Equal(e.HostIDs, []string{w.h2.String(), w.h3.String()}) || e.NoneReason != "" {
		t.Fatalf("enrollment = %+v, want web-2 and web-3", e)
	}
	// Every target with an agent: the section stays, empty.
	w.agents.with[w.h2], w.agents.with[w.h3] = true, true
	e = enrollOf(t, w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}))
	if e.Hosts == nil || e.HostIDs == nil || len(e.Hosts)+len(e.HostIDs) != 0 {
		t.Fatalf("every target with an agent = %+v, want empty lists", e)
	}
}

// A dry run enrols nothing: the section says why and lists no host.
func TestPictureOfADryRunEnrolsNothing(t *testing.T) {
	w := newWorld()
	enrolling(w)
	e := enrollOf(t, w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x, Overrides: &runrequests.Overrides{Check: true, Diff: true}}))
	if e.NoneReason != runrequests.EnrollNoneDryRun || e.Hosts == nil || len(e.Hosts) != 0 || len(e.HostIDs) != 0 {
		t.Fatalf("dry run enrollment = %+v, want none, dry_run", e)
	}
}

// A retry enrols its failed hosts that have no agent.
func TestPictureOfARetryEnrolsItsFailedHosts(t *testing.T) {
	w := newWorld()
	enrolling(w)
	job := id()
	e := enrollOf(t, w.picture(t, runrequests.Request{Kind: runrequests.KindRetryFailed, TemplateID: w.x, JobID: &job}))
	if !slices.Equal(e.Hosts, []string{"web-2"}) || !slices.Equal(e.HostIDs, []string{w.h2.String()}) {
		t.Fatalf("retry enrollment = %+v, want web-2", e)
	}
}

// An approved request shows the list its approval stored, whatever the
// agents are now, so an agent enrolled after the approval does not fail
// the claim; its live picture moves with the agents while pending.
func TestApprovedPictureShowsTheStoredList(t *testing.T) {
	w := newWorld()
	enrolling(w)
	reqID := id()
	pending := runrequests.Request{ID: reqID, Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusPending}
	before := runrequests.Fingerprint(reqID, w.picture(t, pending))
	approved := pending
	approved.Status, approved.PinnedCommit = runrequests.StatusApproved, commitX
	approved.EnrollHostIDs = []uuid.UUID{w.h3, w.h2}
	if runrequests.Fingerprint(reqID, w.picture(t, approved)) != before {
		t.Fatal("the approved picture differs from the one approved")
	}

	w.agents.with[w.h3] = true
	if runrequests.Fingerprint(reqID, w.picture(t, pending)) == before {
		t.Fatal("a pending picture did not change when a target got an agent")
	}
	e := enrollOf(t, w.picture(t, approved))
	if !slices.Equal(e.Hosts, []string{"web-2", "web-3"}) || runrequests.Fingerprint(reqID, w.picture(t, approved)) != before {
		t.Fatalf("approved enrollment after an agent came = %+v, want the stored list", e)
	}
	// A stored host that is no longer a target shows by id.
	gone := id()
	approved.EnrollHostIDs = []uuid.UUID{gone}
	if e := enrollOf(t, w.picture(t, approved)); !slices.Equal(e.Hosts, []string{gone.String()}) || !slices.Equal(e.HostIDs, []string{gone.String()}) {
		t.Fatalf("a stored host that is gone = %+v, want it by id", e)
	}
}

// A failing agent lookup fails the picture: an approval must not miss a
// host it enrols.
func TestPictureFailsWithoutTheAgents(t *testing.T) {
	w := newWorld()
	enrolling(w)
	w.agents.err = errors.New("db down")
	if _, err := w.resolver().Picture(context.Background(), id(), runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}); err == nil {
		t.Fatal("Picture succeeded without the agents")
	}
}

// An approval without a stored list (from before enrollment was switched
// on for the template, or before 00127) shows that the run enrols no
// agents, saying why, not "every target has an agent" (finding 5).
func TestApprovedPictureWithoutAStoredList(t *testing.T) {
	w := newWorld()
	enrolling(w)
	approved := runrequests.Request{ID: id(), Kind: runrequests.KindRun, TemplateID: w.x, Status: runrequests.StatusApproved, PinnedCommit: commitX}
	e := enrollOf(t, w.picture(t, approved))
	if e.NoneReason != runrequests.EnrollNoneApprovedBefore || e.Hosts == nil || len(e.Hosts)+len(e.HostIDs) != 0 {
		t.Fatalf("enrollment = %+v, want none, approved_before_enrollment", e)
	}
}
