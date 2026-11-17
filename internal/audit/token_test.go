package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/paging"
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

// TestSQLStoreNamesPersonalTokens (backlog 3.100): an event made with a
// personal token carries that token's name in the organization's feed and in
// the actor's own, in cursor and in page mode.
func TestSQLStoreNamesPersonalTokens(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	org := seedOrg(t, q, "audit-personal-org")
	actor := seedUser(t, q, "audit-personal@example.test")
	tokenID := uuid.Must(uuid.NewV7())
	if err := q.InsertPersonalToken(ctx, gen.InsertPersonalTokenParams{
		ID: tokenID, UserID: actor, Name: "laptop", SecretHash: []byte("audit-personal-hash"), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	e := audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &org, ActorID: &actor, ActorKind: audit.ActorKindToken,
		TokenID: &tokenID, Action: "POST createHost", ObjectType: "host", Summary: "a", At: time.Now()}
	if err := store.Insert(ctx, e); err != nil {
		t.Fatal(err)
	}
	scoped := db.WithOrgScope(ctx, org)
	page := paging.Page{Number: 1, Size: 10, Sort: audit.SortAt, Desc: true}
	listings := map[string]func() ([]audit.Event, error){
		"List": func() ([]audit.Event, error) {
			items, _, err := store.List(scoped, org, audit.Filter{}, "", 10)
			return items, err
		},
		"ListPage": func() ([]audit.Event, error) {
			items, _, err := store.ListPage(scoped, org, audit.Filter{}, page)
			return items, err
		},
		"ListForActor": func() ([]audit.Event, error) {
			items, _, err := store.ListForActor(ctx, actor, audit.Filter{}, "", 10)
			return items, err
		},
		"ListForActorPage": func() ([]audit.Event, error) {
			items, _, err := store.ListForActorPage(ctx, actor, audit.Filter{}, page)
			return items, err
		},
	}
	for name, list := range listings {
		items, err := list()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(items) != 1 || items[0].TokenID == nil || *items[0].TokenID != tokenID || items[0].TokenName != "laptop" {
			t.Fatalf("%s: %+v", name, items)
		}
	}
}

// TestPageTokenNameHiddenByRLS (controller ruling P8): the name of another
// organization's token is invisible under an organization's scope, so an
// event pointing at one shows no name — in page mode as in cursor mode.
func TestPageTokenNameHiddenByRLS(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := audit.NewSQLStore(d)
	orgA := seedOrg(t, q, "audit-rls-a")
	orgB := seedOrg(t, q, "audit-rls-b")
	actor := seedUser(t, q, "audit-rls@example.test")
	foreign := uuid.Must(uuid.NewV7())
	if err := q.InsertAPIToken(ctx, gen.InsertAPITokenParams{
		ID: foreign, UserID: actor, OrgID: orgB, Name: "b-secret-name", Role: gen.OrgRoleOperator, SecretHash: []byte("audit-rls-hash"),
	}); err != nil {
		t.Fatal(err)
	}
	e := audit.Event{ID: uuid.Must(uuid.NewV7()), OrgID: &orgA, ActorID: &actor, ActorKind: audit.ActorKindToken,
		TokenID: &foreign, Action: "POST createHost", ObjectType: "host", Summary: "a", At: time.Now()}
	if err := store.Insert(ctx, e); err != nil {
		t.Fatal(err)
	}
	inA := db.WithOrgScope(ctx, orgA)
	items, _, err := store.ListPage(inA, orgA, audit.Filter{}, paging.Page{Number: 1, Size: 10, Sort: audit.SortAt, Desc: true})
	if err != nil || len(items) != 1 {
		t.Fatalf("ListPage: %+v, %v", items, err)
	}
	if items[0].TokenID == nil || items[0].TokenName != "" {
		t.Fatalf("page mode: token name %q leaked across organizations", items[0].TokenName)
	}
	cursorItems, _, err := store.List(inA, orgA, audit.Filter{}, "", 10)
	if err != nil || len(cursorItems) != 1 || cursorItems[0].TokenName != "" {
		t.Fatalf("cursor mode: %+v, %v", cursorItems, err)
	}
}
