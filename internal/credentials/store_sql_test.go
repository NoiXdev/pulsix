package credentials_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func seedOrg(t *testing.T, q *gen.Queries, slug string) uuid.UUID {
	t.Helper()
	org, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

func TestSQLStoreRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := credentials.NewSQLStore(d)
	org := seedOrg(t, q, "cred-org")
	other := seedOrg(t, q, "cred-other")
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)

	c := credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "acme-deploy", Kind: credentials.KindSSHKey, Username: "ansible",
		PublicKey: "ssh-ed25519 AAAA", Fingerprint: "SHA256:x", KeyType: "ed25519", ExpiresAt: &expires,
	}
	created, err := store.Create(ctx, c, []byte("sealed-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.CreatedAt.IsZero() || created.ExpiresAt == nil || !created.ExpiresAt.Equal(expires) {
		t.Fatalf("created = %+v", created)
	}
	if _, err := store.Create(ctx, credentials.Credential{ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "acme-deploy", Kind: credentials.KindGitToken}, []byte("x")); !errors.Is(err, credentials.ErrNameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	got, err := store.Get(ctx, org, c.ID)
	if err != nil || got.PublicKey != "ssh-ed25519 AAAA" || got.Kind != credentials.KindSSHKey {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := store.Get(ctx, other, c.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("other org Get: %v", err)
	}
	if list, err := store.List(ctx, org); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}

	got.Name, got.ExpiresAt = "renamed", nil
	updated, err := store.Update(ctx, got, nil)
	if err != nil || updated.Name != "renamed" || updated.ExpiresAt != nil {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	var secret []byte
	if err := d.Pool().QueryRow(ctx, `SELECT secret FROM credentials WHERE id = $1`, c.ID).Scan(&secret); err != nil || string(secret) != "sealed-1" {
		t.Fatalf("a nil secret must keep the stored one: %q, %v", secret, err)
	}
	if _, err := store.Update(ctx, got, []byte("sealed-2")); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, `SELECT secret FROM credentials WHERE id = $1`, c.ID).Scan(&secret); err != nil || string(secret) != "sealed-2" {
		t.Fatalf("secret not replaced: %q, %v", secret, err)
	}
	missing := got
	missing.ID = uuid.Must(uuid.NewV7())
	if _, err := store.Update(ctx, missing, nil); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("Update unknown: %v", err)
	}

	if err := store.Delete(ctx, other, c.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("other org Delete: %v", err)
	}
	if err := store.Delete(ctx, org, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, org, c.ID); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
}
