package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// scriptedGuard answers the claim guard's questions from two scripts, one
// answer per call; the last answer repeats once a script runs out.
type scriptedGuard struct {
	checks   []error
	confirms []bool
	confirm  error
	// checked and confirmed count the calls.
	checked, confirmed int
}

func (g *scriptedGuard) CheckRunClaim(context.Context, uuid.UUID, uuid.UUID, ApprovedRun) error {
	err := g.checks[min(g.checked, len(g.checks)-1)]
	g.checked++
	return err
}

func (g *scriptedGuard) ConfirmWithdrawn(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	ok := g.confirms[min(g.confirmed, len(g.confirms)-1)]
	g.confirmed++
	return ok, g.confirm
}

// TestClaimWithdrawnIsConfirmedUnderTheRequestLock: a job the guard
// refuses as withdrawn is failed as WithdrawnAtClaimError only when the
// guard confirms it inside the failing transaction (backlog 2.127); an
// approval found there lets the guard check the job again, a request that
// keeps flipping fails the job as an ordinary refusal after
// claimGuardRounds, and a failed confirmation fails the claim and leaves
// the job running for the reaper. That the confirmation runs inside the
// failing transaction is pinned against the real request lock by
// TestRunRequestReapprovalWaitsForAWithdrawnClaimAndRefillsTheZone
// (internal/api).
func TestClaimWithdrawnIsConfirmedUnderTheRequestLock(t *testing.T) {
	withdrawn := fmt.Errorf("check the run's request: %w", ErrApprovalWithdrawn)
	tests := []struct {
		name          string
		guard         scriptedGuard
		wantErr       bool
		wantStatus    Status
		wantError     string // exact, when not ""
		notRefused    bool   // the job failed, but not as a refusal
		wantChecked   int
		wantConfirmed int
	}{
		{
			name:       "confirmed withdrawn",
			guard:      scriptedGuard{checks: []error{withdrawn}, confirms: []bool{true}},
			wantStatus: StatusError, wantError: WithdrawnAtClaimError, wantChecked: 1, wantConfirmed: 1,
		},
		{
			name:  "approved meanwhile, checked again and let through",
			guard: scriptedGuard{checks: []error{withdrawn, nil}, confirms: []bool{false}},
			// The job carries no template, so the payload build after the
			// guard fails it — but not as a refusal.
			wantStatus: StatusError, notRefused: true, wantChecked: 2, wantConfirmed: 1,
		},
		{
			name:       "approved meanwhile, then refused for another reason",
			guard:      scriptedGuard{checks: []error{withdrawn, errors.New("picture changed")}, confirms: []bool{false}},
			wantStatus: StatusError, wantError: claimRefusedPrefix + "picture changed", wantChecked: 2, wantConfirmed: 1,
		},
		{
			name:       "never settles",
			guard:      scriptedGuard{checks: []error{withdrawn}, confirms: []bool{false}},
			wantStatus: StatusError, wantError: claimRefusedPrefix + errApprovalUnsettled.Error(),
			wantChecked: claimGuardRounds, wantConfirmed: claimGuardRounds,
		},
		{
			name:    "confirmation fails",
			guard:   scriptedGuard{checks: []error{withdrawn}, confirms: []bool{true}, confirm: errors.New("db down")},
			wantErr: true, wantStatus: StatusRunning, wantChecked: 1, wantConfirmed: 1,
		},
	}
	w := newGroupWorld(t, "jobs-claim-withdrawn-confirm")
	scoped := db.WithOrgScope(context.Background(), w.org)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := Job{
				ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ZoneID: testZone(t, w.d, w.org), Kind: KindRun,
				HostIDs: []uuid.UUID{uuid.Must(uuid.NewV7())}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()),
				Trigger: TriggerManual, CreatedAt: time.Now(),
			}
			if err := NewSQLStore(w.d).CreateJob(scoped, j, nil); err != nil {
				t.Fatal(err)
			}
			guard := tc.guard
			w.q.SetClaimGuard(&guard)
			defer w.q.SetClaimGuard(nil)
			c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{j.ZoneID}, "v1")
			if (err != nil) != tc.wantErr || c != nil {
				t.Fatalf("claim = %v, %v; want no claim, error %v", c, err, tc.wantErr)
			}
			got, err := NewSQLStore(w.d).GetJob(scoped, w.org, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %s %q, want %s", got.Status, got.Error, tc.wantStatus)
			}
			if tc.wantError != "" && got.Error != tc.wantError {
				t.Fatalf("error = %q, want %q", got.Error, tc.wantError)
			}
			if tc.notRefused && (got.Error == "" || strings.HasPrefix(got.Error, claimRefusedPrefix)) {
				t.Fatalf("error = %q, want a payload failure, not a refusal", got.Error)
			}
			if guard.checked != tc.wantChecked || guard.confirmed != tc.wantConfirmed {
				t.Fatalf("guard asked %d times, confirmed %d times; want %d, %d", guard.checked, guard.confirmed, tc.wantChecked, tc.wantConfirmed)
			}
		})
	}
}
