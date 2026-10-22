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
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
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

// TestUsageCountsGitRepoVaultCredentialsDedupedByRepo is the credentials.sql
// gap found during the 2.76/2.92/... batch's own triage: ListCredentialUsage
// counted git_repos.credential_id and .vault_credential_id but never
// git_repo_vault_credentials (migration 00059's labelled vault identity
// link table), so a vault credential used only through a labelled identity
// read as unused in the GUI — the comment directly above the query
// describes closing exactly this class of gap, and v0.2e reopened it one
// layer down.
//
// The second half proves the fix does not double-count: a repository
// carrying both the legacy vault_credential_id slot and a matching
// labelled identity for the SAME credential — the shape every repository
// saved before backlog 2.92 retired the write bridge already has — must
// still count as one repository, not two.
func TestUsageCountsGitRepoVaultCredentialsDedupedByRepo(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "cred-usage-repo-link")
	scoped := db.WithOrgScope(ctx, org)
	credStore := credentials.NewSQLStore(d)
	repoStore := repos.NewSQLStore(d)

	cred, err := credStore.Create(scoped, credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "vault-pw", Kind: credentials.KindVaultPassword,
	}, []byte("sealed"))
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	repo, err := repoStore.Create(scoped, repos.Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", URL: "https://git.example.com/infra.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// Not yet referenced anywhere: the credential reads as unused.
	usage, err := credStore.Usage(ctx, org)
	if err != nil || usage[cred.ID].Repos != 0 {
		t.Fatalf("Usage before any reference = %+v, %v, want Repos 0", usage[cred.ID], err)
	}

	// Referenced only through the labelled vault identity set — no
	// git_repos.credential_id or .vault_credential_id involved at all.
	if err := repoStore.ReplaceRepoVaultIdentities(scoped, org, repo.ID, []repos.VaultIdentity{{CredentialID: cred.ID, Label: "default"}}); err != nil {
		t.Fatalf("ReplaceRepoVaultIdentities: %v", err)
	}
	usage, err = credStore.Usage(ctx, org)
	if err != nil || usage[cred.ID].Repos != 1 {
		t.Fatalf("Usage with only a labelled identity = %+v, %v, want Repos 1", usage[cred.ID], err)
	}
	if err := credStore.Delete(scoped, org, cred.ID); !errors.Is(err, credentials.ErrInUse) {
		t.Fatalf("Delete while only a labelled identity references it = %v, want ErrInUse", err)
	}

	// Now also set the legacy vault_credential_id slot to the SAME
	// credential on the SAME repo — the shape 2.92's now-removed write
	// bridge used to produce, and the shape every repository saved before
	// that fix shipped already has. The repo must still count once.
	if _, err := d.Pool().Exec(ctx, `UPDATE git_repos SET vault_credential_id = $1 WHERE id = $2`, cred.ID, repo.ID); err != nil {
		t.Fatalf("set the legacy slot: %v", err)
	}
	usage, err = credStore.Usage(ctx, org)
	if err != nil || usage[cred.ID].Repos != 1 {
		t.Fatalf("Usage with both the legacy slot and the labelled identity on one repo = %+v, %v, want Repos 1 (deduped), not 2", usage[cred.ID], err)
	}
}

// TestUsageCountsJobTemplateVaultCredentials is
// TestUsageCountsGitRepoVaultCredentialsDedupedByRepo's own gap for
// job_template_vault_credentials (migration 00059's second link table): a
// vault credential a job template adds through its own labelled identity
// set, with no repository involved at all, must also stop reading as
// unused.
func TestUsageCountsJobTemplateVaultCredentials(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "cred-usage-template-link")
	scoped := db.WithOrgScope(ctx, org)
	credStore := credentials.NewSQLStore(d)
	tmplStore := templates.NewSQLStore(d)

	cred, err := credStore.Create(scoped, credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "vault-pw", Kind: credentials.KindVaultPassword,
	}, []byte("sealed"))
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	pb, err := playbooks.NewSQLStore(d).Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Source: playbooks.SourceEditor,
		Inline: strPtr("- hosts: all\n  tasks: []\n"),
	})
	if err != nil {
		t.Fatalf("create playbook: %v", err)
	}
	tmpl, err := tmplStore.Create(scoped, templates.Template{ID: uuid.Must(uuid.NewV7()), OrgID: org, PlaybookID: pb.ID, Name: "deploy"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	usage, err := credStore.Usage(ctx, org)
	if err != nil || usage[cred.ID].Templates != 0 {
		t.Fatalf("Usage before any reference = %+v, %v, want Templates 0", usage[cred.ID], err)
	}

	if err := tmplStore.ReplaceVaultCredentials(scoped, org, tmpl.ID, []templates.VaultCredential{{CredentialID: cred.ID, Label: "team-a"}}); err != nil {
		t.Fatalf("ReplaceVaultCredentials: %v", err)
	}
	usage, err = credStore.Usage(ctx, org)
	if err != nil || usage[cred.ID].Templates != 1 || usage[cred.ID].Repos != 0 {
		t.Fatalf("Usage with a template's labelled identity = %+v, %v, want Templates 1, Repos 0", usage[cred.ID], err)
	}
	if err := credStore.Delete(scoped, org, cred.ID); !errors.Is(err, credentials.ErrInUse) {
		t.Fatalf("Delete while a template's labelled identity references it = %v, want ErrInUse", err)
	}
}

// strPtr is playbooks.Playbook.Inline's own *string field, for a literal
// here rather than a named local in every caller.
func strPtr(s string) *string { return &s }

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
