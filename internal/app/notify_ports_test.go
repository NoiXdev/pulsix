package app

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

type fakeOpenAlerts struct {
	open   []uuid.UUID
	err    error
	scoped bool
}

func (f *fakeOpenAlerts) OpenAlertIDs(ctx context.Context, orgID uuid.UUID, _ []uuid.UUID) ([]uuid.UUID, error) {
	got, ok := db.OrgScope(ctx)
	f.scoped = ok && got == orgID
	return f.open, f.err
}

// TestNotifyAlertStatesReportsWhatIsNotOpen: every asked id that is not
// an open alert is closed, and the read runs in the organization's scope.
func TestNotifyAlertStatesReportsWhatIsNotOpen(t *testing.T) {
	org, a, b, c := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeOpenAlerts{open: []uuid.UUID{a}}
	closed, err := NewNotifyAlertStates(store).ClosedAlerts(context.Background(), org, []uuid.UUID{a, b, c})
	if err != nil || closed[a] || !closed[b] || !closed[c] || !store.scoped {
		t.Fatalf("closed = %v, %v, scoped %v", closed, err, store.scoped)
	}
	store.err = errors.New("down")
	if _, err := NewNotifyAlertStates(store).ClosedAlerts(context.Background(), org, []uuid.UUID{a}); err == nil {
		t.Fatal("error swallowed")
	}
}
