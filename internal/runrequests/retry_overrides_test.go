package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestRetryRequestRepeatsTheRunsExtraVars (final review M1): a retry of a
// run an approved request started repeats that request's extra vars — the
// retry request stores them (so a retry of the retry repeats them too) and
// the approval hands the request with them to the retry; its hosts and limit
// are not repeated, the retry runs on the failed hosts. A run that came from
// no request, or a request without extra vars, has nothing to repeat.
func TestRetryRequestRepeatsTheRunsExtraVars(t *testing.T) {
	ctx := context.Background()
	w := newWiring()
	group := uuid.Must(uuid.NewV7())
	limit := "web-*"
	w.store.rows[group] = runrequests.Request{ID: group, OrgID: w.org, Kind: runrequests.KindRun, Status: runrequests.StatusApproved, RunGroupID: &group,
		Overrides: &runrequests.Overrides{ExtraVars: map[string]any{"version": "1.2"}, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Limit: &limit}}
	w.val.group = group
	job := uuid.Must(uuid.NewV7())
	r, err := w.createWith(runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job})
	if err != nil {
		t.Fatal(err)
	}
	if r.Overrides == nil || r.Overrides.ExtraVars["version"] != "1.2" || r.Overrides.HostIDs != nil || r.Overrides.Limit != nil || r.Overrides.Check {
		t.Fatalf("retry overrides = %+v, want the extra vars only", r.Overrides)
	}
	if stored := w.store.rows[r.ID]; stored.Overrides == nil || stored.Overrides.ExtraVars["version"] != "1.2" {
		t.Fatalf("stored retry overrides = %+v", stored.Overrides)
	}
	_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.enq.request; got.ID != r.ID || got.Overrides == nil || got.Overrides.ExtraVars["version"] != "1.2" {
		t.Fatalf("retry enqueued with %+v", got.Overrides)
	}
	// The person's retry route gets them as checked run overrides.
	ov, err := w.svc.RetryOverrides(ctx, w.org, job)
	if err != nil || ov == nil || !ov.Untrusted || ov.ExtraVars["version"] != "1.2" {
		t.Fatalf("RetryOverrides = %+v %v", ov, err)
	}

	for name, prep := range map[string]func(w *wiring){
		"no request": func(w *wiring) { w.val.group = uuid.Must(uuid.NewV7()) },
		"no extra vars": func(w *wiring) {
			g := uuid.Must(uuid.NewV7())
			w.store.rows[g] = runrequests.Request{ID: g, OrgID: w.org, Kind: runrequests.KindRun, Status: runrequests.StatusApproved, RunGroupID: &g,
				Overrides: &runrequests.Overrides{Check: false, HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}}}
			w.val.group = g
		},
	} {
		w := newWiring()
		prep(w)
		r, err := w.createWith(runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job})
		if err != nil || r.Overrides != nil {
			t.Fatalf("%s: retry overrides = %+v %v, want none", name, r.Overrides, err)
		}
		if ov, err := w.svc.RetryOverrides(ctx, w.org, job); err != nil || ov != nil {
			t.Fatalf("%s: RetryOverrides = %+v %v", name, ov, err)
		}
	}
}

// TestRetryCarriesItsCheckedExtraVars: the resolver's retry picture shows
// the repeated vars and its approval enqueues them untrusted, checked again
// against the template and the retry's hosts (an inventory secret of a
// failed host refuses them).
func TestRetryCarriesItsCheckedExtraVars(t *testing.T) {
	ctx, org := context.Background(), id()
	w := newWorld()
	job := id()
	req := runrequests.Request{ID: id(), Kind: runrequests.KindRetryFailed, TemplateID: w.x, JobID: &job,
		Overrides: &runrequests.Overrides{ExtraVars: map[string]any{"version": "1.2"}}}
	p := w.picture(t, req)
	if p.ExtraVarsOverride["version"] != "1.2" || p.Retry == nil {
		t.Fatalf("retry picture = %+v", p)
	}
	if err := w.resolver().RetryFailed(ctx, org, req, id(), "k", id(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if ov := w.jobs.retryOverrides; ov == nil || !ov.Untrusted || ov.ExtraVars["version"] != "1.2" || ov.Check {
		t.Fatalf("retry overrides = %+v", ov)
	}
	if len(w.invSecrets.asked) != 1 || !slices.Equal(w.invSecrets.asked[0], w.jobs.failed) {
		t.Fatalf("inventory asked about %v, want the failed hosts %v", w.invSecrets.asked, w.jobs.failed)
	}
	w.invSecrets.names["version"] = true
	w.jobs.retryOverrides = nil
	if err := w.resolver().RetryFailed(ctx, org, req, id(), "k", id(), nil, ""); !errors.Is(err, runrequests.ErrInvalidInput) || w.jobs.retryOverrides != nil {
		t.Fatalf("retry over an inventory secret = %v (enqueued %+v)", err, w.jobs.retryOverrides)
	}
}
