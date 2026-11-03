package backup_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
)

// signedObject is the listing entry of a backup this instance wrote: a
// header signed under key, stored under the name ObjectName gives it.
func signedObject(at time.Time, kind string, key []byte) backup.Object {
	h := backup.SignHeader(backup.Header{CreatedAt: at, Kind: kind, PulsixVersion: "1.2.3"}, key)
	return backup.Object{Name: backup.ObjectName(at, kind), Size: 1, Header: h}
}

func TestMarkVerifiedAcceptsSubSecondCreatedAt(t *testing.T) {
	key := backup.HeaderKey(testKey)
	// The name carries whole seconds; the header the full instant.
	o := signedObject(time.Date(2026, 11, 2, 3, 0, 0, 987654321, time.UTC), backup.KindScheduled, key)
	objs := []backup.Object{o}
	backup.MarkVerified(objs, key)
	if !objs[0].Verified {
		t.Fatalf("%s with header time %v is not verified", o.Name, o.Header.CreatedAt)
	}
}

func TestMarkVerifiedRejectsNameKindMismatch(t *testing.T) {
	key := backup.HeaderKey(testKey)
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	o := signedObject(at, backup.KindManual, key)
	o.Name = backup.ObjectName(at, backup.KindScheduled)
	objs := []backup.Object{o}
	backup.MarkVerified(objs, key)
	if objs[0].Verified {
		t.Fatal("a manual backup stored under a scheduled name is verified")
	}
}

func TestMarkVerifiedRejectsNameStampMismatch(t *testing.T) {
	key := backup.HeaderKey(testKey)
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		obj  string
	}{
		{"a second later", backup.ObjectName(at.Add(time.Second), backup.KindScheduled)},
		{"a day later", backup.ObjectName(at.AddDate(0, 0, 1), backup.KindScheduled)},
		{"not a backup name", "copy-of-backup.pxbak"},
		{"a fractional-second alias", "pulsix-20261102T030000.0Z-scheduled.pxbak"},
		{"a nanosecond alias", "pulsix-20261102T030000.000000000Z-scheduled.pxbak"},
		{"a comma alias", "pulsix-20261102T030000,0Z-scheduled.pxbak"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := signedObject(at, backup.KindScheduled, key)
			o.Name = tc.obj
			objs := []backup.Object{o}
			backup.MarkVerified(objs, key)
			if objs[0].Verified {
				t.Fatalf("%s holding a header from %v is verified", o.Name, at)
			}
		})
	}
}

func TestMarkVerifiedRejectsUnsignedForeignAndDamaged(t *testing.T) {
	key := backup.HeaderKey(testKey)
	at := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	unsigned := signedObject(at, backup.KindScheduled, key)
	unsigned.Header.MAC = ""
	foreign := signedObject(at, backup.KindScheduled, backup.HeaderKey([32]byte{7}))
	damaged := signedObject(at, backup.KindScheduled, key)
	damaged.HeaderErr = backup.ErrCorrupt
	objs := []backup.Object{unsigned, foreign, damaged}
	backup.MarkVerified(objs, key)
	for i, what := range []string{"unsigned", "signed under another key", "damaged"} {
		if objs[i].Verified {
			t.Errorf("the %s object is verified", what)
		}
	}
}

// TestListMarksUnsignedBackupUnverified covers Review Focus 1: a backup
// written before headers were signed lists as unverified, and can still be
// downloaded and verified (which is what a restore runs first).
func TestListMarksUnsignedBackupUnverified(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// A backup this build writes: signed, through the service.
	h.clock.Set(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	signed, err := h.svc.Backup(ctx, backup.KindManual, nil, h.storage, true)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-branch backup: a complete file whose header carries no MAC.
	old := craftedHeader()
	data := craft(t, old, []craftedTable{{"a", "1\tx\n", 1}},
		backup.Manifest{Header: old, Tables: []backup.TableCount{{Name: "a", Rows: 1}}})
	oldName := backup.ObjectName(old.CreatedAt, old.Kind)
	h.storage.mu.Lock()
	h.storage.objects[oldName] = data
	h.storage.mu.Unlock()

	entries, err := h.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	verified := map[string]bool{}
	for _, e := range entries {
		if e.State != backup.StateSucceeded {
			t.Errorf("entry %s is %s, want succeeded", e.Name, e.State)
		}
		verified[e.Name] = e.Verified
	}
	if len(verified) != 2 || !verified[signed.Name] || verified[oldName] {
		t.Fatalf("verified = %v, want %s verified and %s not", verified, signed.Name, oldName)
	}

	rc, _, err := h.svc.Open(ctx, oldName)
	if err != nil {
		t.Fatalf("open the unsigned backup: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("the unsigned backup downloads different bytes than were stored")
	}
	if _, err := backup.Verify(bytes.NewReader(got), testKey); err != nil {
		t.Fatalf("Verify of the unsigned backup: %v", err)
	}
	if err := h.svc.Delete(ctx, oldName); err != nil {
		t.Fatalf("delete the unsigned backup: %v", err)
	}
}
