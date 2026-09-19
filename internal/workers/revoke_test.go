package workers_test

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/workers"
)

type fakeStreams struct{ closed []uuid.UUID }

func (f *fakeStreams) CloseWorker(id uuid.UUID) int {
	f.closed = append(f.closed, id)
	return 2
}

type fakeRequeue struct {
	calls []string
	err   error
}

func (f *fakeRequeue) RequeueWorkerJobs(_ context.Context, orgID, workerID uuid.UUID, reason string) (int, int, error) {
	f.calls = append(f.calls, orgID.String()+"/"+workerID.String()+"/"+reason)
	return 3, 1, f.err
}

func TestRevokeCutsCertificatesStreamsAndJobs(t *testing.T) {
	w := newEnrollWorld(t)
	streams, requeue := &fakeStreams{}, &fakeRequeue{}
	w.svc.SetRevokeHooks(streams, requeue)
	got, res, err := w.svc.Revoke(ctx, w.org, w.existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workers.StatusRevoked || res != (workers.RevokeResult{Streams: 2, Requeued: 3, Ended: 1}) {
		t.Fatalf("revoke = %+v, %+v", got, res)
	}
	if len(streams.closed) != 1 || streams.closed[0] != w.existing.ID {
		t.Fatalf("closed = %v", streams.closed)
	}
	if len(requeue.calls) != 1 || requeue.calls[0] != w.org.String()+"/"+w.existing.ID.String()+"/worker revoked" {
		t.Fatalf("requeue = %v", requeue.calls)
	}
	if _, err := w.svc.GetBySerial(ctx, w.existingSerial); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("a revoked worker's serial must be refused: %v", err)
	}
	// A new worker under the same name cannot exist while the row is kept,
	// and after deleting it the old serial stays unknown.
	if err := w.svc.Delete(ctx, w.org, w.existing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.GetBySerial(ctx, w.existingSerial); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if _, _, err := w.svc.Revoke(ctx, w.org, uuid.Must(uuid.NewV7())); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	requeue.err = errors.New("db down")
	other, _ := w.svc.Register(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: "fra-2", CertSerial: big.NewInt(31)})
	if _, _, err := w.svc.Revoke(ctx, w.org, other.ID); err == nil {
		t.Fatal("a failed requeue must be reported")
	}
}

// TestRevokeWithoutHooks: a service built without revoke hooks (the CLI,
// tests) still revokes the row and its certificates.
func TestRevokeWithoutHooks(t *testing.T) {
	w := newEnrollWorld(t)
	got, res, err := w.svc.Revoke(ctx, w.org, w.existing.ID)
	if err != nil || got.Status != workers.StatusRevoked || res != (workers.RevokeResult{}) {
		t.Fatalf("revoke = %+v, %+v, %v", got, res, err)
	}
	if _, err := w.svc.GetBySerial(ctx, w.existingSerial); !errors.Is(err, workers.ErrRevoked) {
		t.Fatalf("revoked serial: %v", err)
	}
	if err := w.svc.Delete(ctx, w.org, uuid.Must(uuid.NewV7())); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("delete unknown: %v", err)
	}
}

func TestListView(t *testing.T) {
	w := newEnrollWorld(t)
	seen := w.now.Add(-time.Minute)
	w.store.mu.Lock()
	w.store.rows[0].LastSeen = &seen
	w.store.mu.Unlock()
	if _, err := w.svc.Register(ctx, w.org, workers.RegisterInput{ZoneID: w.zone, Name: "stale", CertSerial: big.NewInt(32)}); err != nil {
		t.Fatal(err)
	}
	list, err := w.svc.ListView(ctx, w.org)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListView = %v, %v", list, err)
	}
	byName := map[string]workers.View{}
	for _, v := range list {
		byName[v.Name] = v
	}
	if !byName["old-1"].Online || byName["stale"].Online || byName["old-1"].ZoneSlug != "default" {
		t.Fatalf("online/zone = %+v", byName)
	}
	if byName["stale"].CertExpiresAt == nil || !byName["stale"].CertExpiresAt.After(w.now) {
		t.Fatalf("expiry = %v", byName["stale"].CertExpiresAt)
	}
	// A revoked worker is never online, however recently it was seen.
	if _, _, err := w.svc.Revoke(ctx, w.org, w.existing.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = w.svc.ListView(ctx, w.org)
	for _, v := range list {
		if v.Name == "old-1" && (v.Online || v.CertExpiresAt != nil) {
			t.Fatalf("revoked worker = %+v", v)
		}
	}
}
