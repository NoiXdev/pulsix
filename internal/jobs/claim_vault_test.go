package jobs

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// A repository's vault passphrase has to reach the worker the same way
// every other secret does — through the claim's credentials map and the
// redact list — with the job carrying only a pointer to it. Nothing in
// the worker's own tests can see whether the manager actually sets that
// pointer, which is the half this covers.
func TestRunClaimCarriesTheRepositoryVaultCredential(t *testing.T) {
	org, hostID := uuid.New(), uuid.New()
	templateID, repoID := uuid.New(), uuid.New()
	credID, gitCredID, vaultCredID := uuid.New(), uuid.New(), uuid.New()

	creds := &fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:      {PrivateKey: "host-key"},
			gitCredID:   {PrivateKey: "deploy-key"},
			vaultCredID: {Password: "the-vault-passphrase"},
		},
		metas: map[uuid.UUID]credentials.Credential{
			credID:      {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"},
			gitCredID:   {ID: gitCredID, Kind: credentials.KindGitKey},
			vaultCredID: {ID: vaultCredID, Kind: credentials.KindVaultPassword},
		},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookRepo: &repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`)},
	}}
	renderer := secretsRenderer{hostID: hostID, credID: credID}

	q := newTestQueue(renderer, creds, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	q.SetVaultResolver(fakeVaultResolver{})
	// A migrated repository (migration 00059's backfill) has exactly one
	// link row, labelled "default" — the same one-entry set the removed
	// legacy shim used to fabricate from the old vault_credential_id
	// column. The claim now reads it from the labelled set instead.
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {
		ID: repoID, URL: "ssh://git@example.org/x.git", Branch: "main",
		CredentialID: &gitCredID, VaultIdentities: []VaultIdentity{{CredentialID: vaultCredID, Label: "default"}},
	}}})

	var out workerpb.Job
	job := Job{ID: uuid.Must(uuid.NewV7()), Kind: KindRun, OrgID: org, ZoneID: uuid.New(), TemplateID: &templateID, HostIDs: []uuid.UUID{hostID}}
	if err := q.buildPayload(context.Background(), job, &out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	// The identity names the credential's own uuid, so the field says
	// what it is called — unlike the git credential, which the claim
	// ships under "git:<repo-id>" for its own historical reasons.
	identities := out.GetVaultIdentities()
	if len(identities) != 1 || identities[0].GetCredentialId() != vaultCredID.String() || identities[0].GetLabel() != "default" {
		t.Errorf("vault_identities = %v, want one entry {credential_id: %q, label: \"default\"}", identities, vaultCredID)
	}
	raw, ok := out.GetCredentials()[vaultCredID.String()]
	if !ok {
		t.Fatalf("the claim names a vault credential it did not reveal; credentials has %v", keysOf(out.GetCredentials()))
	}
	var secret credentials.Secret
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		t.Fatalf("decode revealed vault secret: %v", err)
	}
	if secret.Password != "the-vault-passphrase" {
		t.Errorf("revealed vault secret = %+v, want the passphrase", secret)
	}

	// And it is redacted like every other revealed secret: a passphrase
	// in a run's stdout would outlive the run in the event log.
	if !slices.Contains(out.GetRedact(), "the-vault-passphrase") {
		t.Errorf("the vault passphrase is not in the redact list: %v", out.GetRedact())
	}
}

func TestRunClaimWithoutARepositoryVaultLeavesTheIdentityListEmpty(t *testing.T) {
	org, hostID := uuid.New(), uuid.New()
	templateID, repoID := uuid.New(), uuid.New()
	credID, gitCredID := uuid.New(), uuid.New()

	creds := &fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {PrivateKey: "host-key"}, gitCredID: {PrivateKey: "deploy-key"}},
		metas: map[uuid.UUID]credentials.Credential{
			credID:    {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"},
			gitCredID: {ID: gitCredID, Kind: credentials.KindGitKey},
		},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookRepo: &repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`)},
	}}
	q := newTestQueue(secretsRenderer{hostID: hostID, credID: credID}, creds, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {
		ID: repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &gitCredID,
	}}})

	var out workerpb.Job
	job := Job{ID: uuid.Must(uuid.NewV7()), Kind: KindRun, OrgID: org, ZoneID: uuid.New(), TemplateID: &templateID, HostIDs: []uuid.UUID{hostID}}
	if err := q.buildPayload(context.Background(), job, &out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if identities := out.GetVaultIdentities(); len(identities) != 0 {
		t.Errorf("vault_identities = %v, want empty for a repository with no vault", identities)
	}
	// No vault resolver was wired either — proof that an unwired resolver
	// only fails a claim that actually needs it (resolveVaultIdentities'
	// own early return), not every repo playbook.
}

// vaultTestFixture builds the shared plumbing (org, host, renderer,
// credentials, bastions) every vault-resolution test below needs, so each
// test can focus on its repo/template vault configuration.
type vaultTestFixture struct {
	org, hostID uuid.UUID
	gitCredID   uuid.UUID
	repoID      uuid.UUID
	creds       *fakeCreds
}

func newVaultTestFixture() *vaultTestFixture {
	org, hostID := uuid.New(), uuid.New()
	credID, gitCredID, repoID := uuid.New(), uuid.New(), uuid.New()
	return &vaultTestFixture{
		org: org, hostID: hostID, gitCredID: gitCredID, repoID: repoID,
		creds: &fakeCreds{
			secrets: map[uuid.UUID]credentials.Secret{
				credID:    {PrivateKey: "host-key"},
				gitCredID: {PrivateKey: "deploy-key"},
			},
			metas: map[uuid.UUID]credentials.Credential{
				credID:    {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"},
				gitCredID: {ID: gitCredID, Kind: credentials.KindGitKey},
			},
		},
	}
}

// addVaultCredential registers a vault credential's secret and metadata,
// so a test can name it in a repo's or template's vault identity set.
func (f *vaultTestFixture) addVaultCredential(id uuid.UUID, password string) {
	f.creds.secrets[id] = credentials.Secret{Password: password}
	f.creds.metas[id] = credentials.Credential{ID: id, Kind: credentials.KindVaultPassword}
}

func (f *vaultTestFixture) queue(templates fakeTemplates) *SQLQueue {
	q := newTestQueue(secretsRenderer{hostID: f.hostID, credID: f.gitCredID}, f.creds, templates, fakeBastions{})
	q.SetKeyPins(fakePins{})
	q.SetVaultResolver(fakeVaultResolver{})
	return q
}

func (f *vaultTestFixture) job(templateID uuid.UUID) Job {
	return Job{ID: uuid.Must(uuid.NewV7()), Kind: KindRun, OrgID: f.org, ZoneID: uuid.New(), TemplateID: &templateID, HostIDs: []uuid.UUID{f.hostID}}
}

// TestRunClaimResolvesRepositoryAndTemplateVaultIdentities is the spec's
// first acceptance criterion: a repository with two labelled vault
// passwords, and a template that adds a third, resolves to three — in
// label order, each carrying its secret — asserted on the workerpb.Job
// the claim actually produces, not on an intermediate.
func TestRunClaimResolvesRepositoryAndTemplateVaultIdentities(t *testing.T) {
	f := newVaultTestFixture()
	prodCred, stagingCred, infraCred := uuid.New(), uuid.New(), uuid.New()
	f.addVaultCredential(prodCred, "prod-pass")
	f.addVaultCredential(stagingCred, "staging-pass")
	f.addVaultCredential(infraCred, "infra-pass")

	templateID := uuid.New()
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: f.org, PlaybookRepo: &f.repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`),
			VaultIdentities: []VaultIdentity{{CredentialID: infraCred, Label: "infra"}},
		},
	}}
	q := f.queue(templates)
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{f.repoID: {
		ID: f.repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &f.gitCredID,
		VaultIdentities: []VaultIdentity{
			{CredentialID: prodCred, Label: "prod"},
			{CredentialID: stagingCred, Label: "staging"},
		},
	}}})

	var out workerpb.Job
	if err := q.buildPayload(context.Background(), f.job(templateID), &out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	identities := out.GetVaultIdentities()
	if len(identities) != 3 {
		t.Fatalf("vault_identities has %d entries, want 3: %v", len(identities), identities)
	}
	wantLabels := []string{"infra", "prod", "staging"} // label order
	for i, want := range wantLabels {
		if identities[i].GetLabel() != want {
			t.Errorf("vault_identities[%d].label = %q, want %q (order: %v)", i, identities[i].GetLabel(), want, identities)
		}
	}
	// The proto carries no from_template bit (that is the run detail's own
	// job, spec decision 3, covered at the API layer); what this asserts
	// is that every label still names the credential its source actually
	// configured.
	wantCred := map[string]uuid.UUID{"infra": infraCred, "prod": prodCred, "staging": stagingCred}
	for _, id := range identities {
		if want := wantCred[id.GetLabel()]; id.GetCredentialId() != want.String() {
			t.Errorf("vault_identities label %q names credential %s, want %s", id.GetLabel(), id.GetCredentialId(), want)
		}
	}
	for _, credID := range []uuid.UUID{prodCred, stagingCred, infraCred} {
		if _, ok := out.GetCredentials()[credID.String()]; !ok {
			t.Errorf("credentials map is missing the secret for %s", credID)
		}
	}
}

// TestRunClaimTemplateVaultIdentityWinsLabelCollision is the spec's third
// acceptance criterion: the same label on both sides resolves to the
// template's credential. The repository's own credential for that label
// must not appear anywhere in the claim — its secret was never the one
// this run was meant to receive.
func TestRunClaimTemplateVaultIdentityWinsLabelCollision(t *testing.T) {
	f := newVaultTestFixture()
	repoCred, templateCred := uuid.New(), uuid.New()
	f.addVaultCredential(repoCred, "repo-prod-pass")
	f.addVaultCredential(templateCred, "template-prod-pass")

	templateID := uuid.New()
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: f.org, PlaybookRepo: &f.repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`),
			VaultIdentities: []VaultIdentity{{CredentialID: templateCred, Label: "prod"}},
		},
	}}
	q := f.queue(templates)
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{f.repoID: {
		ID: f.repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &f.gitCredID,
		VaultIdentities: []VaultIdentity{{CredentialID: repoCred, Label: "prod"}},
	}}})

	var out workerpb.Job
	if err := q.buildPayload(context.Background(), f.job(templateID), &out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	identities := out.GetVaultIdentities()
	if len(identities) != 1 || identities[0].GetLabel() != "prod" || identities[0].GetCredentialId() != templateCred.String() {
		t.Fatalf("vault_identities = %v, want one entry {label: \"prod\", credential_id: %q}", identities, templateCred)
	}
	if _, ok := out.GetCredentials()[repoCred.String()]; ok {
		t.Errorf("the repository's overridden credential %s was revealed; only the template's winning credential should be", repoCred)
	}
	if !slices.Contains(out.GetRedact(), "template-prod-pass") {
		t.Errorf("the winning passphrase is not redacted: %v", out.GetRedact())
	}
	if slices.Contains(out.GetRedact(), "repo-prod-pass") {
		t.Errorf("the overridden passphrase was never opened, yet it is in the redact list: %v", out.GetRedact())
	}
}

// TestSiblingTemplateDoesNotReceiveAnotherTemplatesVaultIdentity is the
// spec's named risk made concrete: two templates share one repository: the
// repository has two vault identities, template A adds a third of its own.
// The claim for template B — the sibling, which configures nothing extra —
// must resolve to exactly the repository's two, never A's third. Nothing
// about this failing would be visible from B's run succeeding; it would
// simply succeed with a password it was never supposed to have.
func TestSiblingTemplateDoesNotReceiveAnotherTemplatesVaultIdentity(t *testing.T) {
	f := newVaultTestFixture()
	prodCred, stagingCred, secretThirdCred := uuid.New(), uuid.New(), uuid.New()
	f.addVaultCredential(prodCred, "prod-pass")
	f.addVaultCredential(stagingCred, "staging-pass")
	f.addVaultCredential(secretThirdCred, "template-a-only-pass")

	templateA, templateB := uuid.New(), uuid.New()
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateA: {
			ID: templateA, OrgID: f.org, PlaybookRepo: &f.repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`),
			VaultIdentities: []VaultIdentity{{CredentialID: secretThirdCred, Label: "infra"}},
		},
		templateB: {
			ID: templateB, OrgID: f.org, PlaybookRepo: &f.repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`),
			// Template B adds nothing of its own: it is the sibling that
			// must stay confined to the repository's own set.
		},
	}}
	repoSource := fakeRepos{byID: map[uuid.UUID]RepoRef{f.repoID: {
		ID: f.repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &f.gitCredID,
		VaultIdentities: []VaultIdentity{
			{CredentialID: prodCred, Label: "prod"},
			{CredentialID: stagingCred, Label: "staging"},
		},
	}}}

	// One shared queue instance serves both claims — a real manager has
	// exactly one SQLQueue, not one per template — so this also catches a
	// per-instance caching mistake (resolving once per repository and
	// reusing the answer for the next template's claim), not only a
	// mistake visible from fresh state.
	q := f.queue(templates)
	q.SetRepoSource(repoSource)

	// Template A's own claim does get the third identity — the setup is
	// correct, so the assertion against B below is a real isolation check,
	// not a trivially-empty resolution.
	var outA workerpb.Job
	if err := q.buildPayload(context.Background(), f.job(templateA), &outA); err != nil {
		t.Fatalf("buildPayload (template A): %v", err)
	}
	if len(outA.GetVaultIdentities()) != 3 {
		t.Fatalf("template A's own claim has %d identities, want 3 (setup is wrong, not the isolation under test): %v", len(outA.GetVaultIdentities()), outA.GetVaultIdentities())
	}

	var outB workerpb.Job
	if err := q.buildPayload(context.Background(), f.job(templateB), &outB); err != nil {
		t.Fatalf("buildPayload (template B, the sibling): %v", err)
	}

	identities := outB.GetVaultIdentities()
	if len(identities) != 2 {
		t.Fatalf("sibling template B's vault_identities has %d entries, want 2 (prod, staging only): %v", len(identities), identities)
	}
	for _, id := range identities {
		if id.GetCredentialId() == secretThirdCred.String() {
			t.Fatalf("sibling template B's claim names template A's own credential %s — the secret leaked across templates sharing one repository", secretThirdCred)
		}
	}
	if _, ok := outB.GetCredentials()[secretThirdCred.String()]; ok {
		t.Fatalf("sibling template B's claim reveals template A's own vault secret in its credentials map")
	}
	if slices.Contains(outB.GetRedact(), "template-a-only-pass") {
		t.Fatalf("template A's passphrase appears in sibling B's redact list, meaning it was opened for a claim that must never see it")
	}
}

// TestRunClaimRedactsEveryVaultPassphrase proves the inherited property
// the brief calls out by name: every vault passphrase a claim reveals must
// be in the redact list, including when two labels legitimately share one
// credential (one secret entry, two identity entries) — a shape a less
// careful implementation could redact only once for and miss nothing, but
// is exactly the case worth pinning since "inherited" properties are the
// ones that quietly stop holding when the shape around them changes.
func TestRunClaimRedactsEveryVaultPassphrase(t *testing.T) {
	f := newVaultTestFixture()
	sharedCred, soloCred := uuid.New(), uuid.New()
	f.addVaultCredential(sharedCred, "shared-pass")
	f.addVaultCredential(soloCred, "solo-pass")

	templateID := uuid.New()
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: f.org, PlaybookRepo: &f.repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`),
			// prod and staging legitimately share one credential under two
			// different labels (spec: a label belongs to the encrypted
			// content, not to the password).
			VaultIdentities: []VaultIdentity{{CredentialID: sharedCred, Label: "staging"}},
		},
	}}
	q := f.queue(templates)
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{f.repoID: {
		ID: f.repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &f.gitCredID,
		VaultIdentities: []VaultIdentity{
			{CredentialID: sharedCred, Label: "prod"},
			{CredentialID: soloCred, Label: "infra"},
		},
	}}})

	var out workerpb.Job
	if err := q.buildPayload(context.Background(), f.job(templateID), &out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	identities := out.GetVaultIdentities()
	if len(identities) != 3 {
		t.Fatalf("vault_identities has %d entries, want 3 (prod, staging, infra): %v", len(identities), identities)
	}
	// Two identities (prod, staging) legitimately name the same credential;
	// the claim must still resolve and reveal it, once, under its own id.
	sharedCount := 0
	for _, id := range identities {
		if id.GetCredentialId() == sharedCred.String() {
			sharedCount++
		}
	}
	if sharedCount != 2 {
		t.Errorf("vault_identities names the shared credential %d times, want 2 (prod and staging)", sharedCount)
	}
	raw, ok := out.GetCredentials()[sharedCred.String()]
	if !ok {
		t.Fatalf("credentials map has no entry for the shared credential %s", sharedCred)
	}
	var sharedSecret credentials.Secret
	if err := json.Unmarshal([]byte(raw), &sharedSecret); err != nil || sharedSecret.Password != "shared-pass" {
		t.Errorf("shared credential's revealed secret = %+v (err %v), want password \"shared-pass\"", sharedSecret, err)
	}
	for _, want := range []string{"shared-pass", "solo-pass"} {
		if !slices.Contains(out.GetRedact(), want) {
			t.Errorf("passphrase %q is not in the redact list: %v", want, out.GetRedact())
		}
	}
}

// TestRunClaimFailsWhenAResolvedVaultCredentialCannotBeOpened pins the
// failure shape: the error names the label and the credential id — what an
// operator needs to fix the wiring — never the password, which never
// exists in this branch to begin with.
func TestRunClaimFailsWhenAResolvedVaultCredentialCannotBeOpened(t *testing.T) {
	f := newVaultTestFixture()
	missingCred := uuid.New()
	// missingCred is named by the repo's vault set but never registered
	// with fakeCreds, so OpenSecret reports credentials.ErrNotFound.

	templateID := uuid.New()
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: f.org, PlaybookRepo: &f.repoID, PlaybookPath: "site.yml",
			Scope: json.RawMessage(`{}`), ExtraVars: json.RawMessage(`{}`)},
	}}
	q := f.queue(templates)
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{f.repoID: {
		ID: f.repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &f.gitCredID,
		VaultIdentities: []VaultIdentity{{CredentialID: missingCred, Label: "prod"}},
	}}})

	var out workerpb.Job
	err := q.buildPayload(context.Background(), f.job(templateID), &out)
	if err == nil {
		t.Fatal("buildPayload succeeded, want an error naming the unopenable vault credential")
	}
	msg := err.Error()
	if !strings.Contains(msg, "prod") || !strings.Contains(msg, missingCred.String()) {
		t.Errorf("error %q does not name both the label %q and the credential id %s", msg, "prod", missingCred)
	}
	if strings.Contains(msg, "the-vault-passphrase") {
		t.Errorf("error %q leaks a password", msg)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// countingCreds wraps fakeCreds and counts OpenSecret calls — the probe
// resolveClaimVaultIdentities's own tests use to prove it never reveals a
// vault secret: that only belongs after the claim's commit (buildPayload),
// never while the row's lock is still held.
type countingCreds struct {
	fakeCreds
	opens int
}

func (c *countingCreds) OpenSecret(ctx context.Context, orgID, id uuid.UUID) (credentials.Secret, error) {
	c.opens++
	return c.fakeCreds.OpenSecret(ctx, orgID, id)
}

// TestResolveClaimVaultIdentitiesMergesWithoutOpeningSecrets pins
// resolveClaimVaultIdentities's own contract (backlog 2.91): it resolves
// the same merge buildPayload will reveal moments later, but it must never
// itself call OpenSecret — that call belongs strictly after the claim's
// commit, never while this function runs inside the still-open transaction
// holding the job row's lock.
func TestResolveClaimVaultIdentitiesMergesWithoutOpeningSecrets(t *testing.T) {
	org, repoID := uuid.New(), uuid.New()
	templateID := uuid.New()
	prodCred, infraCred := uuid.New(), uuid.New()

	creds := &countingCreds{fakeCreds: fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{prodCred: {Password: "prod-pass"}, infraCred: {Password: "infra-pass"}},
	}}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: org, PlaybookRepo: &repoID, PlaybookPath: "site.yml",
			VaultIdentities: []VaultIdentity{{CredentialID: infraCred, Label: "infra"}},
		},
	}}
	q := newTestQueue(secretsRenderer{}, creds, templates, fakeBastions{})
	q.SetVaultResolver(fakeVaultResolver{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {
		ID: repoID, VaultIdentities: []VaultIdentity{{CredentialID: prodCred, Label: "prod"}},
	}}})

	job := Job{ID: uuid.Must(uuid.NewV7()), Kind: KindRun, OrgID: org, TemplateID: &templateID}
	identities, err := q.resolveClaimVaultIdentities(context.Background(), job)
	if err != nil {
		t.Fatalf("resolveClaimVaultIdentities: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("identities = %+v, want 2 (infra, prod)", identities)
	}
	if creds.opens != 0 {
		t.Errorf("OpenSecret was called %d times; resolveClaimVaultIdentities must never open a secret", creds.opens)
	}
}

// TestResolveClaimVaultIdentitiesIsEmptyNotNilWhenThereIsNothingToDecrypt
// pins the distinction migration 00064's own comment relies on: an inline
// playbook, and a repo playbook naming no vault identity on either side,
// both resolve to a non-nil empty slice — a real recorded answer ("nothing
// to decrypt"), never the nil that would be indistinguishable from "not
// resolved at all" once marshalled.
func TestResolveClaimVaultIdentitiesIsEmptyNotNilWhenThereIsNothingToDecrypt(t *testing.T) {
	org := uuid.New()
	inlineTemplateID, repoTemplateID, repoID := uuid.New(), uuid.New(), uuid.New()
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		inlineTemplateID: {ID: inlineTemplateID, OrgID: org, PlaybookInline: "- hosts: all"},
		repoTemplateID:   {ID: repoTemplateID, OrgID: org, PlaybookRepo: &repoID, PlaybookPath: "site.yml"},
	}}
	q := newTestQueue(secretsRenderer{}, fakeCreds{}, templates, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {ID: repoID}}})

	for name, templateID := range map[string]uuid.UUID{"inline playbook": inlineTemplateID, "repo with no vault identity": repoTemplateID} {
		t.Run(name, func(t *testing.T) {
			job := Job{ID: uuid.Must(uuid.NewV7()), Kind: KindRun, OrgID: org, TemplateID: &templateID}
			identities, err := q.resolveClaimVaultIdentities(context.Background(), job)
			if err != nil {
				t.Fatalf("resolveClaimVaultIdentities: %v", err)
			}
			if identities == nil {
				t.Fatal("identities is nil, want a non-nil empty slice")
			}
			if len(identities) != 0 {
				t.Fatalf("identities = %+v, want empty", identities)
			}
		})
	}
}
