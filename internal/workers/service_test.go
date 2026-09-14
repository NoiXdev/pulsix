package workers_test

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/workers"
)

var ctx = context.Background()

type fakeStore struct {
	rows    []workers.Worker
	created workers.Worker
	touched []string
}

func (f *fakeStore) Create(_ context.Context, w workers.Worker) (workers.Worker, error) {
	for _, r := range f.rows {
		if r.OrgID == w.OrgID && r.Name == w.Name {
			return workers.Worker{}, workers.ErrNameTaken
		}
	}
	f.created = w
	f.rows = append(f.rows, w)
	return w, nil
}

func (f *fakeStore) List(_ context.Context, orgID uuid.UUID) ([]workers.Worker, error) {
	var out []workers.Worker
	for _, r := range f.rows {
		if r.OrgID == orgID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) GetBySerial(_ context.Context, serial *big.Int) (workers.Worker, error) {
	for _, r := range f.rows {
		if r.CertSerial.Cmp(serial) == 0 {
			return r, nil
		}
	}
	return workers.Worker{}, workers.ErrNotFound
}

func (f *fakeStore) Touch(_ context.Context, serial *big.Int, version string) error {
	f.touched = append(f.touched, serial.String()+":"+version)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, orgID, id uuid.UUID) error {
	for i, r := range f.rows {
		if r.OrgID == orgID && r.ID == id {
			f.rows = slices.Delete(f.rows, i, i+1)
			return nil
		}
	}
	return workers.ErrNotFound
}

func TestRegisterNameValidation(t *testing.T) {
	serial := big.NewInt(12345)
	tests := []struct {
		name  string
		valid bool
	}{
		{"a", true},
		{"fra-1", true},
		{"0worker", true},
		{"a-b-c-d", true},
		{"", false},
		{"Worker", false},
		{"-worker", false},
		{"wor ker", false},
		{"worker_1", false},
		{"wörker", false},
		{"a012345678901234567890123456789012345678901234567890123456789012", false},
		{"a-0123456789012345678901234567890123456789012345678901234567890", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{}
			svc := workers.NewService(f)
			_, err := svc.Register(ctx, uuid.Must(uuid.NewV7()), workers.RegisterInput{
				ZoneID: uuid.Must(uuid.NewV7()), Name: tt.name, CertSerial: serial,
			})
			if tt.valid && err != nil {
				t.Fatalf("Register(%q): %v", tt.name, err)
			}
			if !tt.valid && !errors.Is(err, workers.ErrInvalidInput) {
				t.Fatalf("Register(%q) = %v, want ErrInvalidInput", tt.name, err)
			}
		})
	}
}

func TestRegisterNilSerialRejected(t *testing.T) {
	f := &fakeStore{}
	svc := workers.NewService(f)
	if _, err := svc.Register(ctx, uuid.Must(uuid.NewV7()), workers.RegisterInput{
		ZoneID: uuid.Must(uuid.NewV7()), Name: "fra-1", CertSerial: nil,
	}); !errors.Is(err, workers.ErrInvalidInput) {
		t.Fatalf("nil serial: %v, want ErrInvalidInput", err)
	}
	if _, err := svc.Register(ctx, uuid.Must(uuid.NewV7()), workers.RegisterInput{
		ZoneID: uuid.Must(uuid.NewV7()), Name: "fra-1", CertSerial: big.NewInt(0),
	}); !errors.Is(err, workers.ErrInvalidInput) {
		t.Fatalf("zero serial: %v, want ErrInvalidInput", err)
	}
}

func TestRegisterZeroZoneRejected(t *testing.T) {
	f := &fakeStore{}
	svc := workers.NewService(f)
	if _, err := svc.Register(ctx, uuid.Must(uuid.NewV7()), workers.RegisterInput{
		Name: "fra-1", CertSerial: big.NewInt(42),
	}); !errors.Is(err, workers.ErrInvalidInput) {
		t.Fatalf("zero zone: %v, want ErrInvalidInput", err)
	}
}

func TestRegisterHappyPathReturnsStoreResult(t *testing.T) {
	f := &fakeStore{}
	svc := workers.NewService(f)
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	serial := big.NewInt(987654321)
	w, err := svc.Register(ctx, org, workers.RegisterInput{
		ZoneID: zone, Name: "fra-1", CertSerial: serial, Version: "0.2.0",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if f.created.OrgID != org || f.created.ZoneID != zone || f.created.Name != "fra-1" ||
		f.created.CertSerial.Cmp(serial) != 0 || f.created.Version != "0.2.0" || f.created.ID == uuid.Nil {
		t.Fatalf("store received = %+v", f.created)
	}
	if w.ID != f.created.ID || w.CertSerial.Cmp(serial) != 0 {
		t.Fatalf("Register = %+v, want the stored worker %+v", w, f.created)
	}
}

func TestListDelegates(t *testing.T) {
	f := &fakeStore{}
	svc := workers.NewService(f)
	org := uuid.Must(uuid.NewV7())
	serial1, serial2 := big.NewInt(1), big.NewInt(2)
	for _, in := range []workers.RegisterInput{
		{ZoneID: uuid.Must(uuid.NewV7()), Name: "fra-1", CertSerial: serial1},
		{ZoneID: uuid.Must(uuid.NewV7()), Name: "fra-2", CertSerial: serial2},
	} {
		if _, err := svc.Register(ctx, org, in); err != nil {
			t.Fatal(err)
		}
	}
	list, err := svc.List(ctx, org)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
	empty, err := svc.List(ctx, uuid.Must(uuid.NewV7()))
	if err != nil || len(empty) != 0 {
		t.Fatalf("List of another org = %v, %v, want nothing", empty, err)
	}
}

func TestDeleteMapsNotFound(t *testing.T) {
	f := &fakeStore{}
	svc := workers.NewService(f)
	org, id := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if err := svc.Delete(ctx, org, id); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("Delete unknown: %v, want ErrNotFound", err)
	}
	w, err := svc.Register(ctx, org, workers.RegisterInput{
		ZoneID: uuid.Must(uuid.NewV7()), Name: "fra-1", CertSerial: big.NewInt(7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, org, w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, org, w.ID); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("second Delete: %v, want ErrNotFound", err)
	}
	if _, err := f.GetBySerial(ctx, big.NewInt(7)); !errors.Is(err, workers.ErrNotFound) {
		t.Fatalf("worker still resolvable after delete: %v, want ErrNotFound", err)
	}
}

func TestTouchDelegates(t *testing.T) {
	f := &fakeStore{}
	svc := workers.NewService(f)
	if err := svc.Touch(ctx, big.NewInt(11), "0.2.0"); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if len(f.touched) != 1 || f.touched[0] != "11:0.2.0" {
		t.Fatalf("touched = %v", f.touched)
	}
	if err := svc.Touch(ctx, big.NewInt(12), ""); err != nil {
		t.Fatalf("Touch with empty version: %v", err)
	}
}
