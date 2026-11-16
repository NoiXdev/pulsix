package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestSQLStoreRecordsTheToken: an event made with an API token carries the
// token's id, and both listings resolve its name (spec 2026-11-16
// assistant access, "Audit").
func TestSQLStoreRecordsTheToken(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-token-org")
	actor := seedUser(t, q, "audit-token@example.test")
	tokenID := uuid.Must(uuid.NewV7())
	if err := q.InsertAPIToken(ctx, gen.InsertAPITokenParams{
		ID: tokenID, UserID: actor, OrgID: org, Name: "assistant", Role: gen.OrgRoleOperator,
		SecretHash: []byte("audit-token-hash"), Kind: "assistant", Areas: []string{"jobs.read"},
	}); err != nil {
		t.Fatal(err)
	}
	withToken := audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &actor, ActorKind: audit.ActorKindToken,
		TokenID: &tokenID, Action: "POST createInventoryGroup", ObjectType: "inventory_group", Summary: "a", At: time.Now()}
	bySession := audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &actor, ActorKind: audit.ActorKindUser,
		Action: "POST createInventoryGroup", ObjectType: "inventory_group", Summary: "b", At: time.Now()}
	for _, e := range []audit.Event{withToken, bySession} {
		if err := store.Insert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, items []audit.Event) {
		t.Helper()
		if len(items) != 2 {
			t.Fatalf("%s: %d items", name, len(items))
		}
		for _, e := range items {
			switch e.ID {
			case withToken.ID:
				if e.TokenID == nil || *e.TokenID != tokenID || e.TokenName != "assistant" {
					t.Errorf("%s: token event = %+v", name, e)
				}
			case bySession.ID:
				if e.TokenID != nil || e.TokenName != "" {
					t.Errorf("%s: session event = %+v", name, e)
				}
			}
		}
	}
	items, _, err := store.List(ctx, org, audit.Filter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	check("List", items)
	items, _, err = store.ListForActor(ctx, actor, audit.Filter{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	check("ListForActor", items)
}
