package repos_test

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
)

// --- ResolveVaultIdentities: table-driven, no database ---

func TestResolveVaultIdentitiesRepositoryOnly(t *testing.T) {
	credA := uuid.Must(uuid.NewV7())
	repo := []repos.VaultIdentity{{CredentialID: credA, Label: "prod"}}
	got := repos.ResolveVaultIdentities(repo, nil)
	want := []repos.VaultIdentity{{CredentialID: credA, Label: "prod", FromTemplate: false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveVaultIdentitiesTemplateOnly(t *testing.T) {
	credA := uuid.Must(uuid.NewV7())
	tmpl := []repos.VaultIdentity{{CredentialID: credA, Label: "prod"}}
	got := repos.ResolveVaultIdentities(nil, tmpl)
	want := []repos.VaultIdentity{{CredentialID: credA, Label: "prod", FromTemplate: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestResolveVaultIdentitiesNeither(t *testing.T) {
	got := repos.ResolveVaultIdentities(nil, nil)
	if len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
}

// TestResolveVaultIdentitiesDisjointMergeIsSortedByLabel pins both the union
// and the ordering: without the sort, the merge is a Go map iteration, which
// is deliberately randomised, and the same job's ansible.cfg would differ
// between two runs for no reason.
func TestResolveVaultIdentitiesDisjointMergeIsSortedByLabel(t *testing.T) {
	credStaging, credProd, credQA := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	repo := []repos.VaultIdentity{
		{CredentialID: credStaging, Label: "staging"},
		{CredentialID: credProd, Label: "prod"},
	}
	tmpl := []repos.VaultIdentity{{CredentialID: credQA, Label: "qa"}}

	got := repos.ResolveVaultIdentities(repo, tmpl)
	want := []repos.VaultIdentity{
		{CredentialID: credProd, Label: "prod", FromTemplate: false},
		{CredentialID: credQA, Label: "qa", FromTemplate: true},
		{CredentialID: credStaging, Label: "staging", FromTemplate: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestResolveVaultIdentitiesTemplateWinsCollision is Review Focus 2: the
// same label on both sides resolves to the template's credential, and
// FromTemplate is true for it — getting this backwards is invisible until a
// run decrypts with the wrong password.
func TestResolveVaultIdentitiesTemplateWinsCollision(t *testing.T) {
	credRepo, credTemplate := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	repo := []repos.VaultIdentity{{CredentialID: credRepo, Label: "prod"}}
	tmpl := []repos.VaultIdentity{{CredentialID: credTemplate, Label: "prod"}}

	got := repos.ResolveVaultIdentities(repo, tmpl)
	want := []repos.VaultIdentity{{CredentialID: credTemplate, Label: "prod", FromTemplate: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v (the template's credential must win)", got, want)
	}
}

// TestResolveVaultIdentitiesSameLabelSameCredentialYieldsOneEntry: the two
// sources agreeing is not a collision worth two rows — the label is still a
// map key, so the second write (the template's) simply confirms the first.
func TestResolveVaultIdentitiesSameLabelSameCredentialYieldsOneEntry(t *testing.T) {
	cred := uuid.Must(uuid.NewV7())
	repo := []repos.VaultIdentity{{CredentialID: cred, Label: "prod"}}
	tmpl := []repos.VaultIdentity{{CredentialID: cred, Label: "prod"}}

	got := repos.ResolveVaultIdentities(repo, tmpl)
	want := []repos.VaultIdentity{{CredentialID: cred, Label: "prod", FromTemplate: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want exactly one entry %+v", got, want)
	}
}

// TestResolveVaultIdentitiesUntouchedRepositoryLabelKeepsFromTemplateFalse:
// a repository identity the template does not name at all must not be
// mistaken for one the template contributed.
func TestResolveVaultIdentitiesUntouchedRepositoryLabelKeepsFromTemplateFalse(t *testing.T) {
	credStaging, credQA := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	repo := []repos.VaultIdentity{{CredentialID: credStaging, Label: "staging"}}
	tmpl := []repos.VaultIdentity{{CredentialID: credQA, Label: "qa"}}

	got := repos.ResolveVaultIdentities(repo, tmpl)
	for _, id := range got {
		if id.Label == "staging" && id.FromTemplate {
			t.Fatalf("got %+v, staging must keep FromTemplate false", got)
		}
	}
}

// --- The store round-trip (step 5) ---

// seedVaultCredential inserts one vault_password credential in org, through
// the real credentials store — the kind git_repo_vault_credentials' and
// job_template_vault_credentials' own composite foreign keys need something
// of to point at. seedGitCredential (store_sql_test.go) already covers the
// git_key kind this file does not need.
func seedVaultCredential(t *testing.T, d *db.DB, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	c, err := credentials.NewSQLStore(d).Create(db.WithOrgScope(ctx, orgID), credentials.Credential{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Kind: credentials.KindVaultPassword,
	}, []byte("sealed-material"))
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedRepoBackedTemplate creates a template whose playbook is sourced from
// repoID, through the real playbooks and templates stores — the shape the
// round-trip test below needs for "two templates on one repository".
func seedRepoBackedTemplate(t *testing.T, d *db.DB, orgID, repoID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	scoped := db.WithOrgScope(ctx, orgID)
	path := "site.yml"
	pb, err := playbooks.NewSQLStore(d).Create(scoped, playbooks.Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Source: playbooks.SourceRepo, RepoID: &repoID, Path: &path,
	})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.NewSQLStore(d).Create(scoped, templates.Template{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, PlaybookID: pb.ID, Name: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tmpl.ID
}

// TestStoreRoundTripVaultIdentitiesStayPerTemplate is Review Focus 1: a
// template's identities must not appear in a sibling template's resolved
// set on the same repository. A secret reaching a run that should not have
// it is the failure this whole sub-project risks, and it is silent — the
// run simply succeeds with more power than intended.
func TestStoreRoundTripVaultIdentitiesStayPerTemplate(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "vault-rt-org")
	scoped := db.WithOrgScope(ctx, org)
	repoStore := repos.NewSQLStore(d)
	tmplStore := templates.NewSQLStore(d)

	repo, err := repoStore.Create(scoped, repos.Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", URL: "https://git.example.com/infra.git",
	})
	if err != nil {
		t.Fatalf("Create repo: %v", err)
	}
	credDefault := seedVaultCredential(t, d, org, "default vault")
	if err := repoStore.ReplaceRepoVaultIdentities(scoped, org, repo.ID,
		[]repos.VaultIdentity{{CredentialID: credDefault, Label: "default"}}); err != nil {
		t.Fatalf("ReplaceRepoVaultIdentities: %v", err)
	}

	templateA := seedRepoBackedTemplate(t, d, org, repo.ID, "deploy-a")
	templateB := seedRepoBackedTemplate(t, d, org, repo.ID, "deploy-b")
	credA := seedVaultCredential(t, d, org, "team a vault")
	credB := seedVaultCredential(t, d, org, "team b vault")
	if err := tmplStore.ReplaceVaultCredentials(scoped, org, templateA,
		[]templates.VaultCredential{{CredentialID: credA, Label: "team-a"}}); err != nil {
		t.Fatalf("ReplaceVaultCredentials A: %v", err)
	}
	if err := tmplStore.ReplaceVaultCredentials(scoped, org, templateB,
		[]templates.VaultCredential{{CredentialID: credB, Label: "team-b"}}); err != nil {
		t.Fatalf("ReplaceVaultCredentials B: %v", err)
	}

	repoIdentities, err := repoStore.ListRepoVaultIdentities(scoped, org, repo.ID)
	if err != nil {
		t.Fatalf("ListRepoVaultIdentities: %v", err)
	}

	resolveFor := func(templateID uuid.UUID) []repos.VaultIdentity {
		t.Helper()
		rows, err := tmplStore.ListVaultCredentials(scoped, org, templateID)
		if err != nil {
			t.Fatalf("ListVaultCredentials: %v", err)
		}
		tmplIdentities := make([]repos.VaultIdentity, len(rows))
		for i, r := range rows {
			tmplIdentities[i] = repos.VaultIdentity{CredentialID: r.CredentialID, Label: r.Label}
		}
		return repos.ResolveVaultIdentities(repoIdentities, tmplIdentities)
	}

	wantA := []repos.VaultIdentity{
		{CredentialID: credDefault, Label: "default", FromTemplate: false},
		{CredentialID: credA, Label: "team-a", FromTemplate: true},
	}
	if gotA := resolveFor(templateA); !reflect.DeepEqual(gotA, wantA) {
		t.Fatalf("template A resolved = %+v, want %+v", gotA, wantA)
	}

	wantB := []repos.VaultIdentity{
		{CredentialID: credDefault, Label: "default", FromTemplate: false},
		{CredentialID: credB, Label: "team-b", FromTemplate: true},
	}
	if gotB := resolveFor(templateB); !reflect.DeepEqual(gotB, wantB) {
		t.Fatalf("template B resolved = %+v, want %+v", gotB, wantB)
	}
}

// TestStoreRoundTripVaultIdentitiesReplaceDropsStaleLabels proves Replace is
// a true replacement, not an append: labels present before a second call
// but absent from it are gone, the way UpdateCheckRule's own replace-a-set
// callers expect.
func TestStoreRoundTripVaultIdentitiesReplaceDropsStaleLabels(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "vault-replace-org")
	scoped := db.WithOrgScope(ctx, org)
	store := repos.NewSQLStore(d)

	repo, err := store.Create(scoped, repos.Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "infra", URL: "https://git.example.com/infra.git",
	})
	if err != nil {
		t.Fatalf("Create repo: %v", err)
	}
	credOld, credNew := seedVaultCredential(t, d, org, "old vault"), seedVaultCredential(t, d, org, "new vault")

	if err := store.ReplaceRepoVaultIdentities(scoped, org, repo.ID,
		[]repos.VaultIdentity{{CredentialID: credOld, Label: "prod"}, {CredentialID: credOld, Label: "staging"}}); err != nil {
		t.Fatalf("first ReplaceRepoVaultIdentities: %v", err)
	}
	if err := store.ReplaceRepoVaultIdentities(scoped, org, repo.ID,
		[]repos.VaultIdentity{{CredentialID: credNew, Label: "prod"}}); err != nil {
		t.Fatalf("second ReplaceRepoVaultIdentities: %v", err)
	}

	got, err := store.ListRepoVaultIdentities(scoped, org, repo.ID)
	if err != nil {
		t.Fatalf("ListRepoVaultIdentities: %v", err)
	}
	want := []repos.VaultIdentity{{CredentialID: credNew, Label: "prod"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v (staging must be dropped, prod repointed)", got, want)
	}

	// Replacing down to zero is a legal "no vault passwords" result.
	if err := store.ReplaceRepoVaultIdentities(scoped, org, repo.ID, nil); err != nil {
		t.Fatalf("replace to empty: %v", err)
	}
	if got, err := store.ListRepoVaultIdentities(scoped, org, repo.ID); err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v, want empty", got, err)
	}
}
