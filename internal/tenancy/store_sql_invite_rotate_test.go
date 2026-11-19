package tenancy_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestSQLStoreRotateAndRestoreInvite covers the resend's storage (spec
// 2026-11-19 §4): rotating swaps the link, expiry and issuer of an
// unaccepted invitation of the organization, expired or not, and reports
// what it replaced; restoring puts that back only while the rotated link is
// still the current one.
func TestSQLStoreRotateAndRestoreInvite(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := tenancy.NewSQLStore(d)

	org := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "rotate", Name: "Rotate"}
	other := tenancy.Org{ID: uuid.Must(uuid.NewV7()), Slug: "rotate-other", Name: "Other"}
	for _, o := range []tenancy.Org{org, other} {
		if err := store.CreateOrg(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	inviter := seedUser(t, q, "rotate-inviter@example.test")
	resender := seedUser(t, q, "rotate-resender@example.test")
	for _, u := range []uuid.UUID{inviter, resender} {
		if err := store.AddMembership(ctx, org.ID, u, tenancy.RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	expired := tenancy.Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: org.ID, Email: "rotate@example.test", Role: tenancy.RoleOperator,
		InvitedBy: inviter, ExpiresAt: now.Add(-time.Hour),
	}
	if err := store.InsertInvite(ctx, expired, hashOf("old")); err != nil {
		t.Fatal(err)
	}

	next := tenancy.InviteRotation{TokenHash: hashOf("new"), ExpiresAt: now.Add(tenancy.InviteTTL), InvitedBy: resender}
	if _, _, err := store.RotateInvite(ctx, other.ID, expired.ID, next); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("rotate through another organization: %v, want ErrNotFound", err)
	}
	if _, _, err := store.RotateInvite(ctx, org.ID, uuid.Must(uuid.NewV7()), next); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("rotate an unknown invitation: %v, want ErrNotFound", err)
	}
	got, prev, err := store.RotateInvite(ctx, org.ID, expired.ID, next)
	if err != nil {
		t.Fatalf("RotateInvite: %v", err)
	}
	if got.ID != expired.ID || got.OrgID != org.ID || got.Email != expired.Email || got.Role != tenancy.RoleOperator ||
		got.InvitedBy != resender || !got.ExpiresAt.Equal(next.ExpiresAt) {
		t.Fatalf("rotated invite = %+v", got)
	}
	if !bytes.Equal(prev.TokenHash, hashOf("old")) || !prev.ExpiresAt.Equal(expired.ExpiresAt) || prev.InvitedBy != inviter {
		t.Fatalf("replaced values = %+v", prev)
	}
	if _, err := store.PeekInvite(ctx, hashOf("old"), now); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("old link after rotating: %v, want ErrNotFound", err)
	}
	if _, err := store.PeekInvite(ctx, hashOf("new"), now); err != nil {
		t.Fatalf("new link after rotating: %v", err)
	}

	// A restore that names a link the invitation no longer carries leaves
	// it alone; the right one puts the old values back.
	if err := store.RestoreInvite(ctx, org.ID, expired.ID, hashOf("stale"), prev); err != nil {
		t.Fatalf("RestoreInvite with a stale link: %v", err)
	}
	if _, err := store.PeekInvite(ctx, hashOf("new"), now); err != nil {
		t.Fatalf("a stale restore replaced the current link: %v", err)
	}
	if err := store.RestoreInvite(ctx, org.ID, expired.ID, hashOf("new"), prev); err != nil {
		t.Fatalf("RestoreInvite: %v", err)
	}
	restored, err := store.PeekInvite(ctx, hashOf("old"), now.Add(-2*time.Hour))
	if err != nil || restored.InvitedBy != inviter || !restored.ExpiresAt.Equal(expired.ExpiresAt) {
		t.Fatalf("restored invite = %+v, %v", restored, err)
	}

	// An accepted invitation is history: it cannot be rotated.
	if _, _, err := store.RotateInvite(ctx, org.ID, expired.ID, next); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TakeInvite(ctx, hashOf("new"), now); err != nil {
		t.Fatal(err)
	}
	again := tenancy.InviteRotation{TokenHash: hashOf("newer"), ExpiresAt: now.Add(tenancy.InviteTTL), InvitedBy: resender}
	if _, _, err := store.RotateInvite(ctx, org.ID, expired.ID, again); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("rotate an accepted invitation: %v, want ErrNotFound", err)
	}
}
