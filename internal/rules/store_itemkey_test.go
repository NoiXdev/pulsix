package rules_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// rawItemCheckKey reads check_rules.item_check_key straight from the table,
// so a test can tell NULL from a stored string.
func rawItemCheckKey(t *testing.T, d *db.DB, id uuid.UUID) *string {
	t.Helper()
	var v *string
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT item_check_key FROM check_rules WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestRuleStoreItemCheckKeyRoundTrip (backlog 3.13e): the column round-trips
// on create and update, and the empty key — the plugin's — is stored as
// NULL, the value every rule written before the column existed carries.
func TestRuleStoreItemCheckKeyRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-item-check-key")
	host := seedHost(t, d, org, "h1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	r := hostRule(org, host)
	r.Scope, r.Item, r.ItemCheckKey = rules.ScopeItem, "/data", "disk-data"
	if err := st.Create(scoped, r); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(scoped, org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemCheckKey != "disk-data" {
		t.Fatalf("ItemCheckKey = %q, want disk-data", got.ItemCheckKey)
	}

	r.ItemCheckKey = "disk-backup"
	if err := st.Update(scoped, r); err != nil {
		t.Fatal(err)
	}
	list, err := st.List(scoped, org, rules.Filter{Scope: rules.ScopeItem})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ItemCheckKey != "disk-backup" {
		t.Fatalf("list = %+v, want the one item rule with the updated key", list)
	}

	r.ItemCheckKey = ""
	if err := st.Update(scoped, r); err != nil {
		t.Fatal(err)
	}
	if v := rawItemCheckKey(t, d, r.ID); v != nil {
		t.Fatalf("item_check_key = %q, want NULL for the plugin's key", *v)
	}
	if got, err := st.Get(scoped, org, r.ID); err != nil || got.ItemCheckKey != "" {
		t.Fatalf("Get = %+v, %v; want an empty ItemCheckKey", got, err)
	}

	plain := hostRule(org, host)
	if err := st.Create(scoped, plain); err != nil {
		t.Fatal(err)
	}
	if v := rawItemCheckKey(t, d, plain.ID); v != nil {
		t.Fatalf("a host rule's item_check_key = %q, want NULL", *v)
	}
}

// TestRuleStoreItemCheckKeyCheckConstraint: the migration's CHECK keeps the
// column NULL on every scope but item, whatever the service lets through.
func TestRuleStoreItemCheckKeyCheckConstraint(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rules-item-check-key-check")
	host := seedHost(t, d, org, "h1")
	group := seedGroup(t, d, org, "g1")
	st := rules.NewSQLStore(d)
	scoped := db.WithOrgScope(context.Background(), org)

	base := hostRule(org, host)
	base.ItemCheckKey = "cpu-2"

	hostScoped := base
	hostScoped.ID = uuid.Must(uuid.NewV7())
	if err := st.Create(scoped, hostScoped); !isCheckViolation(err) {
		t.Fatalf("host scope with item_check_key = %v, want a check_violation", err)
	}
	orgScoped := base
	orgScoped.ID, orgScoped.Scope, orgScoped.HostID = uuid.Must(uuid.NewV7()), rules.ScopeOrg, nil
	if err := st.Create(scoped, orgScoped); !isCheckViolation(err) {
		t.Fatalf("org scope with item_check_key = %v, want a check_violation", err)
	}
	groupScoped := base
	groupScoped.ID, groupScoped.Scope, groupScoped.HostID, groupScoped.GroupID = uuid.Must(uuid.NewV7()), rules.ScopeGroup, nil, &group
	if err := st.Create(scoped, groupScoped); !isCheckViolation(err) {
		t.Fatalf("group scope with item_check_key = %v, want a check_violation", err)
	}

	item := base
	item.ID, item.Scope, item.Item = uuid.Must(uuid.NewV7()), rules.ScopeItem, "x"
	if err := st.Create(scoped, item); err != nil {
		t.Fatalf("item scope with item_check_key: %v", err)
	}
	// Turning the item rule into a host rule while it keeps the key is
	// refused on update too.
	item.Scope, item.Item = rules.ScopeHost, ""
	if err := st.Update(scoped, item); !isCheckViolation(err) {
		t.Fatalf("update to host scope keeping item_check_key = %v, want a check_violation", err)
	}
}
