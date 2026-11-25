package triggers_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/triggers"
)

// An approval writes the trigger in the transaction that records the
// decision: the store's create, update and secret change join a bound
// write transaction, so a rollback leaves no trigger and no change behind.
func TestStoreWritesJoinTheBoundTransaction(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-tx")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit())
	rollback := errors.New("roll back")
	err := d.WithWriteTx(scoped, org, func(ctx context.Context) error {
		if _, err := st.Create(ctx, in, []byte("sealed")); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("tx = %v", err)
	}
	if _, err := st.Get(scoped, org, in.ID); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("a rolled back create left the trigger: %v", err)
	}
	created, err := st.Create(scoped, in, []byte("sealed"))
	if err != nil {
		t.Fatal(err)
	}
	err = d.WithWriteTx(scoped, org, func(ctx context.Context) error {
		up := created
		up.Name = "renamed"
		if _, err := st.Update(ctx, up, created.Revision); err != nil {
			return err
		}
		if _, err := st.SetSecretAt(ctx, org, in.ID, created.Revision+1, []byte("other")); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("tx = %v", err)
	}
	got, secret, err := st.Resolve(ctx, in.ID)
	if err != nil || got.Name != created.Name || got.Revision != created.Revision || string(secret) != "sealed" {
		t.Fatalf("after the rollback = %+v %q %v", got, secret, err)
	}
}

// SetSecretAt replaces the secret only at the revision given; Disable
// turns a trigger off (a new revision) and leaves a disabled one alone.
func TestStoreSetSecretAtAndDisable(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-at")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit())
	created, err := st.Create(scoped, in, []byte("sealed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSecretAt(scoped, org, in.ID, created.Revision+1, []byte("x")); !errors.Is(err, triggers.ErrRevisionMismatch) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, err := st.SetSecretAt(scoped, org, uuid.Must(uuid.NewV7()), 1, []byte("x")); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("unknown trigger: %v", err)
	}
	rotated, err := st.SetSecretAt(scoped, org, in.ID, created.Revision, []byte("new"))
	if err != nil || rotated.Revision != created.Revision+1 {
		t.Fatalf("SetSecretAt = %+v %v", rotated, err)
	}
	if _, secret, _ := st.Resolve(ctx, in.ID); string(secret) != "new" {
		t.Fatalf("secret = %q", secret)
	}
	off, err := st.Disable(scoped, org, in.ID)
	if err != nil || off.Enabled || off.Revision != rotated.Revision+1 {
		t.Fatalf("Disable = %+v %v", off, err)
	}
	again, err := st.Disable(scoped, org, in.ID)
	if err != nil || again.Enabled || again.Revision != off.Revision {
		t.Fatalf("Disable of a disabled trigger = %+v %v", again, err)
	}
	if _, err := st.Disable(scoped, org, uuid.Must(uuid.NewV7())); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("unknown trigger: %v", err)
	}
}
