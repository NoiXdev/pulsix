// worker add --replace: re-enrolling a worker whose name a restored
// database already knows.
package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/workers"
)

// TestWorkerAddReplaceReenrolsSameName (integration): a restored database
// already knows the worker's name. Without --replace that is the registry's
// ErrNameTaken; with it the old worker is revoked and deleted and the new
// certificate's serial is registered under the same name.
func TestWorkerAddReplaceReenrolsSameName(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	orgID, zoneID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: orgID, Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneID, OrgID: orgID, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	enroll := func(replace bool) (*x509.Certificate, error) {
		var stdout bytes.Buffer
		err := RunWorkerAdd(ctx, cfg, WorkerAddOptions{ZoneSlug: "default", Name: "worker-1", Replace: replace}, &stdout, io.Discard)
		if err != nil {
			return nil, err
		}
		certPEM, keyPEM, _, err := splitPEMSections(stdout.String())
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return x509.ParseCertificate(pair.Certificate[0])
	}

	old, err := enroll(false)
	if err != nil {
		t.Fatalf("first enrollment: %v", err)
	}
	if _, err := enroll(false); !errors.Is(err, workers.ErrNameTaken) {
		t.Fatalf("second enrollment without --replace: %v, want ErrNameTaken", err)
	}
	renewed, err := enroll(true)
	if err != nil {
		t.Fatalf("enrollment with --replace: %v", err)
	}

	appDB, err := db.OpenAsRole(ctx, cfg.DatabaseURL, "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	defer appDB.Close()
	svc := workers.NewService(workers.NewSQLStore(appDB))
	w, err := svc.GetBySerial(ctx, renewed.SerialNumber)
	if err != nil || w.Name != "worker-1" || w.Status != workers.StatusActive {
		t.Fatalf("the new serial resolves to %+v, %v; want active worker-1", w, err)
	}
	if _, err := svc.GetBySerial(ctx, old.SerialNumber); err == nil {
		t.Error("the replaced worker's certificate still resolves")
	}
	list, err := svc.List(ctx, orgID)
	if err != nil || len(list) != 1 {
		t.Errorf("workers after --replace = %v, %v; want exactly one", list, err)
	}
}

// fakeReplaceRegistry is the registry --replace works on.
type fakeReplaceRegistry struct {
	workers           []workers.Worker
	revoked, deleted  []uuid.UUID
	registerCalls     int
	registerTakenOnce bool
}

func (f *fakeReplaceRegistry) Register(context.Context, uuid.UUID, string, uuid.UUID, *big.Int) error {
	f.registerCalls++
	if f.registerCalls == 1 && f.registerTakenOnce {
		return fmt.Errorf("register worker: %w", workers.ErrNameTaken)
	}
	return nil
}

func (f *fakeReplaceRegistry) List(context.Context, uuid.UUID) ([]workers.Worker, error) {
	return f.workers, nil
}

func (f *fakeReplaceRegistry) Revoke(_ context.Context, _, id uuid.UUID) (workers.Worker, workers.RevokeResult, error) {
	f.revoked = append(f.revoked, id)
	return workers.Worker{}, workers.RevokeResult{}, nil
}

func (f *fakeReplaceRegistry) Delete(_ context.Context, _, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// TestReplacingRegisterLeavesARevokedWorkerAlone: --replace undoes no
// revocation; a revoked worker of that name is deleted on the Workers page.
func TestReplacingRegisterLeavesARevokedWorkerAlone(t *testing.T) {
	ctx := context.Background()
	id := uuid.Must(uuid.NewV7())
	f := &fakeReplaceRegistry{
		registerTakenOnce: true,
		workers:           []workers.Worker{{ID: id, Name: "worker-1", Status: workers.StatusRevoked}},
	}
	err := replacingRegister{reg: f, registry: f, stderr: io.Discard}.Register(ctx, uuid.Nil, "worker-1", uuid.Nil, big.NewInt(1))
	if err == nil || !strings.Contains(err.Error(), "Workers page") {
		t.Fatalf("replacing a revoked worker: %v, want a refusal pointing at the Workers page", err)
	}
	if len(f.revoked)+len(f.deleted) != 0 || f.registerCalls != 1 {
		t.Errorf("revoked %v deleted %v registered %d times; want nothing touched", f.revoked, f.deleted, f.registerCalls)
	}

	f = &fakeReplaceRegistry{
		registerTakenOnce: true,
		workers:           []workers.Worker{{ID: id, Name: "worker-1", Status: workers.StatusActive}},
	}
	if err := (replacingRegister{reg: f, registry: f, stderr: io.Discard}).Register(ctx, uuid.Nil, "worker-1", uuid.Nil, big.NewInt(1)); err != nil {
		t.Fatalf("replacing an active worker: %v", err)
	}
	if len(f.revoked) != 1 || len(f.deleted) != 1 || f.registerCalls != 2 {
		t.Errorf("revoked %v deleted %v registered %d times; want revoke, delete, register again", f.revoked, f.deleted, f.registerCalls)
	}
}
