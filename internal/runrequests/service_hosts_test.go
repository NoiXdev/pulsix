package runrequests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestCheckRunClaimComparesTheJobsHosts (backlog 2.141): a job of an
// approved run is claimed only while every host it renders is among the
// approved picture's targets; fewer is fine, any other host refuses the
// claim as an ordinary failure of the run, not as a withdrawn approval.
func TestCheckRunClaimComparesTheJobsHosts(t *testing.T) {
	ctx := context.Background()
	h1, h2, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	w := newWiring()
	w.pics.p.Targets = []runrequests.PictureHost{{ID: h1.String(), Name: "a"}, {ID: h2.String(), Name: "b"}}
	r := w.create(t)
	_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil, true); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		hosts   []uuid.UUID
		refused bool
	}{
		{"every approved host", []uuid.UUID{h2, h1}, false},
		{"fewer hosts", []uuid.UUID{h1}, false},
		{"no hosts", nil, false},
		{"a host not approved", []uuid.UUID{h1, other}, true},
		{"only a host not approved", []uuid.UUID{other}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := w.svc.CheckRunClaim(ctx, w.org, r.ID, jobs.ApprovedRun{HostIDs: tc.hosts})
			if !tc.refused {
				if err != nil {
					t.Fatalf("claim = %v, want it let through", err)
				}
				return
			}
			if !errors.Is(err, runrequests.ErrHostsChangedAfterApproval) || errors.Is(err, jobs.ErrApprovalWithdrawn) {
				t.Fatalf("claim = %v, want ErrHostsChangedAfterApproval", err)
			}
		})
	}
}
