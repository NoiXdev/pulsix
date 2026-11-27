package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// followUpClaimFx is an approved run of X on h1 and h2 whose picture
// starts Y (bound to h2) and Y's follow-up Z (bound to h2), and skips the
// deselectable W; groups name the follow-up runs the chain started.
type followUpClaimFx struct {
	w                 *wiring
	r                 runrequests.Request
	x, y, z, wt       uuid.UUID
	h1, h2            uuid.UUID
	gY, gZ, gW, gLate uuid.UUID
}

func newFollowUpClaimFx(t *testing.T, onlyY bool) followUpClaimFx {
	t.Helper()
	f := followUpClaimFx{w: newWiring(), x: id(), y: id(), z: id(), wt: id(), h1: id(), h2: id(), gY: id(), gZ: id(), gW: id(), gLate: id()}
	h2 := []runrequests.PictureHost{{ID: f.h2.String(), Name: "web-1"}}
	z := runrequests.WithChainPath(runrequests.PictureFollowUp{TemplateID: f.z.String(), TemplateName: "Z", Targets: h2, VarsDigest: "d-z"}, f.x, f.y)
	y := runrequests.WithChainPath(runrequests.PictureFollowUp{TemplateID: f.y.String(), TemplateName: "Y", Targets: h2, VarsDigest: "d-y",
		FollowUps: []runrequests.PictureFollowUp{z}}, f.x)
	w := runrequests.WithChainPath(runrequests.PictureFollowUp{TemplateID: f.wt.String(), TemplateName: "W", VarsDigest: "d-w"}, f.x)
	f.w.pics.p.Targets = []runrequests.PictureHost{{ID: f.h1.String(), Name: "db-1"}, h2[0]}
	f.w.pics.p.FollowUps = &runrequests.PictureFollowUps{Start: true, Templates: []runrequests.PictureFollowUp{y, w}}
	f.w.pics.live = map[uuid.UUID]runrequests.PictureFollowUp{f.y: y, f.z: z, f.wt: w}
	f.r = f.w.create(t)
	ctx := context.Background()
	_, _, fp, _ := f.w.svc.Get(ctx, f.w.org, f.r.ID)
	var selection *[]uuid.UUID
	if onlyY {
		selection = &[]uuid.UUID{f.y}
	}
	if _, err := f.w.svc.Approve(ctx, f.w.org, f.r.ID, id(), fp, selection, true); err != nil {
		t.Fatal(err)
	}
	f.w.store.origins = map[uuid.UUID]runrequests.FollowUpOrigin{
		f.gY:    {RequestID: f.r.ID, Path: []uuid.UUID{f.x}, TemplateID: f.y},
		f.gZ:    {RequestID: f.r.ID, Path: []uuid.UUID{f.x, f.y}, TemplateID: f.z},
		f.gW:    {RequestID: f.r.ID, Path: []uuid.UUID{f.x}, TemplateID: f.wt},
		f.gLate: {RequestID: f.r.ID, Path: []uuid.UUID{f.x, f.y}, TemplateID: id()},
	}
	return f
}

// TestCheckRunClaimOfAFollowUp is backlog 2.143: the claim of a job of a
// follow-up run an approval started compares the follow-up's live hosts
// and vars with what the approval bound, and its hosts with the bound
// ones, as 2.141 does for the run itself; refusals are ordinary failures.
func TestCheckRunClaimOfAFollowUp(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		group  func(followUpClaimFx) uuid.UUID
		hosts  func(followUpClaimFx) []uuid.UUID
		change func(followUpClaimFx)
		want   error
	}{
		{"unchanged", func(f followUpClaimFx) uuid.UUID { return f.gY }, func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h2} }, nil, nil},
		{"a follow-up of a follow-up", func(f followUpClaimFx) uuid.UUID { return f.gZ }, func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h2} }, nil, nil},
		{"a host the binding does not hold", func(f followUpClaimFx) uuid.UUID { return f.gY },
			func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h2, f.h1} }, nil, runrequests.ErrHostsChangedAfterApproval},
		{"its vars changed", func(f followUpClaimFx) uuid.UUID { return f.gY }, func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h2} },
			func(f followUpClaimFx) {
				n := f.w.pics.live[f.y]
				n.VarsDigest = "d-y-2"
				f.w.pics.live[f.y] = n
			}, runrequests.ErrChangedAfterApproval},
		{"its hosts changed", func(f followUpClaimFx) uuid.UUID { return f.gZ }, func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h2} },
			func(f followUpClaimFx) {
				n := f.w.pics.live[f.z]
				n.Targets = nil
				f.w.pics.live[f.z] = n
			}, runrequests.ErrChangedAfterApproval},
		{"a follow-up the approval did not show", func(f followUpClaimFx) uuid.UUID { return f.gLate },
			func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h2} }, nil, runrequests.ErrChangedAfterApproval},
		{"no run request's chain", func(followUpClaimFx) uuid.UUID { return id() }, func(f followUpClaimFx) []uuid.UUID { return []uuid.UUID{f.h1} }, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFollowUpClaimFx(t, false)
			if tc.change != nil {
				tc.change(f)
			}
			err := f.w.svc.CheckRunClaim(ctx, f.w.org, tc.group(f), jobs.ApprovedRun{HostIDs: tc.hosts(f)})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("claim = %v, want it let through", err)
				}
				return
			}
			if !errors.Is(err, tc.want) || errors.Is(err, jobs.ErrApprovalWithdrawn) {
				t.Fatalf("claim = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCheckRunClaimOfAFollowUpChecksAgainstTheParentsHosts: the live
// binding is taken among the hosts the approval stored for it, its
// parent's bound hosts (the run's targets for a direct follow-up).
func TestCheckRunClaimOfAFollowUpChecksAgainstTheParentsHosts(t *testing.T) {
	f := newFollowUpClaimFx(t, false)
	ctx := context.Background()
	for group, want := range map[uuid.UUID][]uuid.UUID{f.gY: {f.h1, f.h2}, f.gZ: {f.h2}} {
		if err := f.w.svc.CheckRunClaim(ctx, f.w.org, group, jobs.ApprovedRun{HostIDs: []uuid.UUID{f.h2}}); err != nil {
			t.Fatal(err)
		}
		tmpl := f.w.store.origins[group].TemplateID
		if got := f.w.pics.within[tmpl]; !slices.Equal(got, want) {
			t.Fatalf("within of %s = %v, want %v", tmpl, got, want)
		}
	}
}

// TestCheckRunClaimOfADeselectedFollowUp: a follow-up the person
// deselected is not bound; a run of it is refused.
func TestCheckRunClaimOfADeselectedFollowUp(t *testing.T) {
	f := newFollowUpClaimFx(t, true)
	err := f.w.svc.CheckRunClaim(context.Background(), f.w.org, f.gW, jobs.ApprovedRun{})
	if !errors.Is(err, runrequests.ErrChangedAfterApproval) {
		t.Fatalf("claim of the deselected follow-up = %v, want refused", err)
	}
	if err := f.w.svc.CheckRunClaim(context.Background(), f.w.org, f.gY, jobs.ApprovedRun{HostIDs: []uuid.UUID{f.h2}}); err != nil {
		t.Fatalf("claim of the selected follow-up = %v", err)
	}
}

// TestCheckRunClaimOfAFollowUpOfALegacyOrWithdrawnApproval: an approval
// made before bindings existed lets its follow-ups through as before; a
// request that is no longer approved refuses them, as an ordinary
// failure, never as a withdrawn approval of the request's own run.
func TestCheckRunClaimOfAFollowUpOfALegacyOrWithdrawnApproval(t *testing.T) {
	ctx := context.Background()
	f := newFollowUpClaimFx(t, false)
	r := f.w.store.rows[f.r.ID]
	r.FollowUpBindings = nil
	f.w.store.rows[f.r.ID] = r
	if err := f.w.svc.CheckRunClaim(ctx, f.w.org, f.gLate, jobs.ApprovedRun{HostIDs: []uuid.UUID{f.h1}}); err != nil {
		t.Fatalf("legacy approval's follow-up = %v, want it let through", err)
	}
	if err := f.w.store.RevertToPending(ctx, f.w.org, f.r.ID); err != nil {
		t.Fatal(err)
	}
	err := f.w.svc.CheckRunClaim(ctx, f.w.org, f.gY, jobs.ApprovedRun{HostIDs: []uuid.UUID{f.h2}})
	if err == nil || errors.Is(err, jobs.ErrApprovalWithdrawn) {
		t.Fatalf("follow-up of a request no longer approved = %v, want an ordinary refusal", err)
	}
}
