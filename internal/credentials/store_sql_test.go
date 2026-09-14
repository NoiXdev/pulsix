package credentials_test

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
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
	if usage, err := store.Usage(ctx, org); err != nil || usage[c.ID] != (credentials.Usage{}) {
		t.Fatalf("Usage of an unreferenced credential = %v, %v", usage, err)
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

// TestOpenSecretGeneratedKeyRoundTrip is the brief's integration test for
// backlog 2.17: a generated SSH key goes through Create (sealed) and comes
// back through OpenSecret as a parseable private key — the exact path the
// job claim's payload build takes.
func TestOpenSecretGeneratedKeyRoundTrip(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "cred-opensecret-org")
	svc := credentials.NewService(credentials.NewSQLStore(d), crypto.NewBox([32]byte{'t', 'e', 's', 't', '-', 'm', 'a', 's', 't', 'e', 'r', '-', 'k', 'e', 'y', '0', '1', '2', '3', '4'}), time.Now, rand.Reader)
	created, err := svc.Create(ctx, org, credentials.CreateInput{
		Name: "run-key", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	secret, err := svc.OpenSecret(ctx, org, created.ID)
	if err != nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if _, err := gossh.ParsePrivateKey([]byte(secret.PrivateKey)); err != nil {
		t.Fatalf("opened private key does not parse: %v", err)
	}
	if _, err := svc.OpenSecret(ctx, org, uuid.Must(uuid.NewV7())); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("unknown credential: %v", err)
	}
}

// TestGetCredentialSecretSelectsNoMetadata is the regression test backlog
// 2.17 asks for: the generated SQL of GetCredentialSecret must select
// exactly kind and secret — never SELECT *, which would silently widen the
// one secret-bearing read into a metadata read.
func TestGetCredentialSecretSelectsNoMetadata(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "db", "gen", "credentials.sql.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`const getCredentialSecret = ` + "`([^`]*)`").FindSubmatch(raw)
	if m == nil {
		t.Fatal("getCredentialSecret query not found in gen/credentials.sql.go")
	}
	sql := string(m[1])
	if !strings.Contains(sql, "SELECT kind, secret FROM credentials") {
		t.Fatalf("GetCredentialSecret must select exactly kind and secret, got:\n%s", sql)
	}
	if strings.Contains(sql, "SELECT *") {
		t.Fatalf("GetCredentialSecret must not select *:\n%s", sql)
	}
}
