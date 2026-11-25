package inventory_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// TestSecretVarNames: the names of the secret fields of every group a host
// belongs to, its ancestors' included (their vars reach the host too),
// whether the secret is group- or host-scoped. A group the hosts are not
// in counts for nothing.
func TestSecretVarNames(t *testing.T) {
	f := newFixture()
	prod := f.group(t, "production", nil)
	web := f.group(t, "webserver", &prod.ID)
	other := f.group(t, "other", nil)
	setSchema := func(id uuid.UUID, rev int64, fields ...schema.Field) {
		t.Helper()
		if _, _, err := f.svc.SetSchema(ctx, f.org, id, rev, schema.Schema{Fields: fields}); err != nil {
			t.Fatal(err)
		}
	}
	setSchema(prod.ID, prod.Revision,
		schema.Field{Name: "db_pass", Type: schema.TypeSecret},
		schema.Field{Name: "region", Type: schema.TypeString},
		schema.Field{Name: "smtp", Type: schema.TypeObject, Fields: []schema.Field{{Name: "password", Type: schema.TypeSecret}}})
	setSchema(web.ID, web.Revision, schema.Field{Name: "tls_key", Type: schema.TypeSecret, Scope: schema.ScopeHost})
	setSchema(other.ID, other.Revision, schema.Field{Name: "other_secret", Type: schema.TypeSecret})
	if _, err := f.svc.Assign(ctx, f.org, web.ID, f.web01, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.SecretVarNames(ctx, f.org, []uuid.UUID{f.web01})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"db_pass", "smtp", "tls_key"} {
		if !got[name] {
			t.Errorf("%s missing from %v", name, got)
		}
	}
	for _, name := range []string{"region", "other_secret"} {
		if got[name] {
			t.Errorf("%s in %v", name, got)
		}
	}
	if got, err := f.svc.SecretVarNames(ctx, f.org, nil); err != nil || len(got) != 0 {
		t.Fatalf("no hosts = %v %v", got, err)
	}
}
