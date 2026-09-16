package inventory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// syncGroupStore wraps the real SQLStore and pauses the first ListGroups
// call the renderer makes — the earliest read renderIn does inside
// Store.WithReadTx's transaction — so the test can commit a conflicting
// write from an entirely separate transaction while the render is
// in-flight, then let the read continue.
type syncGroupStore struct {
	*inventory.SQLStore
	ready   chan struct{}
	proceed chan struct{}
}

func (w *syncGroupStore) ListGroups(ctx context.Context, orgID uuid.UUID) ([]inventory.Group, error) {
	close(w.ready)
	<-w.proceed
	return w.SQLStore.ListGroups(ctx, orgID)
}

// TestRenderReadsOneRepeatableReadSnapshot is backlog 2.16: the renderer's
// reads (groups, memberships, hosts, SSH settings, jump hosts, credentials)
// used to be separate statements, each seeing whatever was committed at
// the moment it ran. This pauses the render right after
// Store.WithReadTx has bound the transaction (WithReadTx's own first
// statement, set_config, is what fixes a REPEATABLE READ transaction's
// snapshot in PostgreSQL) but before the renderer's own first read
// (ListGroups) executes, commits a change to the very row that read is
// about to fetch from a fully separate, already-committed transaction, and
// then lets the read proceed. A render still isolated to one snapshot must
// return the value from before the concurrent commit; a later, fresh
// render must return the new value, proving the write really did land and
// the first render's answer was isolation, not a bug that misses writes.
func TestRenderReadsOneRepeatableReadSnapshot(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	hostStore := hosts.NewSQLStore(d)
	sshStore := sshconf.NewSQLStore(d)
	credStore := credentials.NewSQLStore(d)
	invSvc := inventory.NewService(inventory.NewSQLStore(d), hostStore, sshStore, testBox())

	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: "render-snapshot", Name: "render-snapshot"})
	if err != nil {
		t.Fatal(err)
	}

	sc := schema.Schema{Fields: []schema.Field{{Name: "x", Type: schema.TypeString}}}
	g, err := invSvc.CreateGroup(ctx, org.ID, inventory.GroupInput{Name: "grp"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	g, _, err = invSvc.SetSchema(ctx, org.ID, g.ID, g.Revision, sc)
	if err != nil {
		t.Fatalf("set schema: %v", err)
	}
	g, err = invSvc.SetVars(ctx, org.ID, g.ID, g.Revision, map[string]any{"x": "before"})
	if err != nil {
		t.Fatalf("set vars: %v", err)
	}

	wrapped := &syncGroupStore{SQLStore: inventory.NewSQLStore(d), ready: make(chan struct{}), proceed: make(chan struct{})}
	renderer := inventory.NewRenderer(wrapped, hostStore, sshStore, credStore, testBox())

	type result struct {
		inv render.Inventory
		err error
	}
	done := make(chan result, 1)
	go func() {
		inv, err := renderer.Preview(ctx, org.ID, render.Scope{})
		done <- result{inv, err}
	}()

	<-wrapped.ready
	if _, err := invSvc.SetVars(ctx, org.ID, g.ID, g.Revision, map[string]any{"x": "after"}); err != nil {
		t.Fatalf("concurrent SetVars: %v", err)
	}
	close(wrapped.proceed)

	r := <-done
	if r.err != nil {
		t.Fatalf("Preview: %v", r.err)
	}
	grp, ok := r.inv.Doc["grp"].(map[string]any)
	if !ok {
		t.Fatalf("Doc[grp] missing: %+v", r.inv.Doc)
	}
	if got := grp["vars"].(map[string]any)["x"]; got != "before" {
		t.Fatalf("in-flight render vars[x] = %v, want the pre-write snapshot value %q", got, "before")
	}

	// A fresh render afterwards must see the write. It uses a plain,
	// unwrapped renderer — the syncGroupStore's channels are one-shot and
	// the point of this second call is only to confirm the write landed.
	freshRenderer := inventory.NewRenderer(inventory.NewSQLStore(d), hostStore, sshStore, credStore, testBox())
	inv2, err := freshRenderer.Preview(ctx, org.ID, render.Scope{})
	if err != nil {
		t.Fatalf("second Preview: %v", err)
	}
	if got := inv2.Doc["grp"].(map[string]any)["vars"].(map[string]any)["x"]; got != "after" {
		t.Fatalf("fresh render vars[x] = %v, want %q (the write must have actually landed)", got, "after")
	}
}
