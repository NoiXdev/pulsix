package jobs

import (
	"context"
	"encoding/json"
	"slices"
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
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {
		ID: repoID, URL: "ssh://git@example.org/x.git", Branch: "main",
		CredentialID: &gitCredID, VaultCredentialID: &vaultCredID,
	}}})

	var out workerpb.Job
	job := Job{ID: uuid.Must(uuid.NewV7()), Kind: KindRun, OrgID: org, ZoneID: uuid.New(), TemplateID: &templateID, HostIDs: []uuid.UUID{hostID}}
	if err := q.buildPayload(context.Background(), job, &out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	// The pointer names the credential's own uuid, so the field says what
	// it is called — unlike the git credential, which the claim ships
	// under "git:<repo-id>" for its own historical reasons.
	if out.GetVaultCredentialId() != vaultCredID.String() {
		t.Errorf("vault_credential_id = %q, want the credential's uuid %q", out.GetVaultCredentialId(), vaultCredID)
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

func TestRunClaimWithoutARepositoryVaultLeavesThePointerEmpty(t *testing.T) {
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
	if out.GetVaultCredentialId() != "" {
		t.Errorf("vault_credential_id = %q, want empty for a repository with no vault", out.GetVaultCredentialId())
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
