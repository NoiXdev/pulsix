package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/workers"
)

// embeddedRowStore is the slice of workers.Store RegisterOrAttach touches;
// any other method panics through the nil embedded interface.
type embeddedRowStore struct {
	workers.Store
	row      *workers.Worker
	getErr   error
	created  bool
	attached int
}

func (s *embeddedRowStore) GetByName(context.Context, uuid.UUID, string) (workers.Worker, error) {
	if s.getErr != nil {
		return workers.Worker{}, s.getErr
	}
	if s.row == nil {
		return workers.Worker{}, workers.ErrNotFound
	}
	return *s.row, nil
}

func (s *embeddedRowStore) Create(_ context.Context, w workers.Worker, _ workers.Certificate) (workers.Worker, error) {
	s.created = true
	return w, nil
}

func (s *embeddedRowStore) AddCertificate(context.Context, workers.Certificate) error {
	s.attached++
	return nil
}

// TestRegisterEmbedded: the embedded worker runs on a new or reusable row
// and stays down, with a log line naming the fix, on a revoked row, a row
// in another zone (Task 12 review, minor 4) or a store failure.
func TestRegisterEmbedded(t *testing.T) {
	zone := zoneRow{OrgID: uuid.Must(uuid.NewV7()), ID: uuid.Must(uuid.NewV7())}
	row := func(zoneID uuid.UUID, st workers.Status) *workers.Worker {
		return &workers.Worker{ID: uuid.Must(uuid.NewV7()), OrgID: zone.OrgID, ZoneID: zoneID, Name: workers.EmbeddedName, Status: st}
	}
	for _, tc := range []struct {
		name    string
		store   *embeddedRowStore
		wantRun bool
		wantLog string
	}{
		{"first start", &embeddedRowStore{}, true, ""},
		{"restart", &embeddedRowStore{row: row(zone.ID, workers.StatusActive)}, true, ""},
		{"revoked", &embeddedRowStore{row: row(zone.ID, workers.StatusRevoked)}, false, "revoked; delete it on the Workers page"},
		{"another zone", &embeddedRowStore{row: row(uuid.Must(uuid.NewV7()), workers.StatusActive)}, false, "enrolled in another zone; delete it on the Workers page"},
		{"store failure", &embeddedRowStore{getErr: errors.New("db down")}, false, "db down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			got := registerEmbedded(t.Context(), workers.NewService(tc.store), zone, big.NewInt(42), log)
			if got != tc.wantRun {
				t.Fatalf("run = %v, want %v (log %q)", got, tc.wantRun, buf.String())
			}
			if tc.wantLog != "" && !strings.Contains(buf.String(), tc.wantLog) {
				t.Fatalf("log %q must contain %q", buf.String(), tc.wantLog)
			}
			if tc.wantRun && !tc.store.created && tc.store.attached != 1 {
				t.Fatal("a running embedded worker must have its certificate recorded")
			}
			if !tc.wantRun && (tc.store.created || tc.store.attached != 0) {
				t.Fatal("a refused embedded worker must not record its certificate")
			}
		})
	}
}
