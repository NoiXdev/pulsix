package alerts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
)

// forgetCall is one CloseForgotten call the fake store saw.
type forgetCall struct {
	tx             pgx.Tx
	org, host      uuid.UUID
	checkKey, item string
	at             time.Time
}

func (f *fakeStore) CloseForgotten(_ context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, checkKey, item string, now time.Time) (int, error) {
	f.forgetCalls = append(f.forgetCalls, forgetCall{tx: tx, org: orgID, host: hostID, checkKey: checkKey, item: item, at: now})
	return f.forgetN, f.forgetErr
}

// TestCloseForgottenPassesTheCallersTransaction: Service.CloseForgotten
// adds no decision of its own — it runs on the caller's transaction (the
// forget's, which also deletes the item's rows) at the caller's instant,
// and reports the store's count.
func TestCloseForgottenPassesTheCallersTransaction(t *testing.T) {
	store := newFakeStore()
	store.forgetN = 2
	clockNow := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	svc := alerts.NewService(store, clockAt(clockNow))
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	at := clockNow.Add(-time.Minute)
	var tx pgx.Tx // the fake never touches it; identity is what is checked

	n, err := svc.CloseForgotten(context.Background(), tx, org, host, "disk", "/", at)
	if err != nil {
		t.Fatalf("close forgotten: %v", err)
	}
	if n != 2 {
		t.Fatalf("n = %d, want the store's 2", n)
	}
	want := forgetCall{tx: tx, org: org, host: host, checkKey: "disk", item: "/", at: at}
	if len(store.forgetCalls) != 1 || store.forgetCalls[0] != want {
		t.Fatalf("store calls = %+v, want exactly %+v", store.forgetCalls, want)
	}
}

func TestCloseForgottenWrapsTheStoreError(t *testing.T) {
	store := newFakeStore()
	boom := errors.New("boom")
	store.forgetErr = boom
	svc := alerts.NewService(store, clockAt(time.Now()))

	_, err := svc.CloseForgotten(context.Background(), nil, uuid.New(), uuid.New(), "disk", "/", time.Now())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the store's error", err)
	}
}
