package repos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope. d is kept alongside q (not
// just q built once over d) so ReplaceRepoVaultIdentities can open its own
// transaction — every other method here is a single statement and has no
// need of one.
type SQLStore struct {
	q   *gen.Queries
	d   *db.DB
	log *slog.Logger
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

// InTx implements Store: one organization-scoped write transaction that
// every store call made with the context it passes to fn joins, since
// this store's *db.DB picks a bound transaction up from the context
// (db.WithWriteTx). Nested use is not supported — db.beginOrg always
// begins a fresh pool transaction rather than joining one already bound,
// so an InTx inside an InTx would take a second connection and could
// block on the locks the outer one holds.
func (s *SQLStore) InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	return s.d.WithWriteTx(ctx, orgID, fn)
}

func (s *SQLStore) Create(ctx context.Context, r Repo) (Repo, error) {
	row, err := s.q.CreateRepo(ctx, gen.CreateRepoParams{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, Url: r.URL, Branch: r.Branch,
		Subpath: r.Subpath, CredentialID: db.NullUUID(r.CredentialID),
		VaultCredentialID: db.NullUUID(r.VaultCredentialID),
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Repo{}, ErrNameTaken
		}
		return Repo{}, fmt.Errorf("create repo: %w", err)
	}
	return toRepo(row), nil
}

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Repo, error) {
	row, err := s.q.GetRepo(ctx, gen.GetRepoParams{OrgID: orgID, ID: id})
	if err != nil {
		return Repo{}, mapErr(err)
	}
	return toRepo(row), nil
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Repo, error) {
	rows, err := s.q.ListRepos(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	out := make([]Repo, len(rows))
	for i, r := range rows {
		out[i] = toRepo(r)
	}
	return out, nil
}

func (s *SQLStore) Update(ctx context.Context, r Repo) (Repo, error) {
	var hostKey, hostPub *string
	if r.SSHHostKey != "" {
		hostKey = &r.SSHHostKey
	}
	if r.SSHHostPublicKey != "" {
		hostPub = &r.SSHHostPublicKey
	}
	row, err := s.q.UpdateRepo(ctx, gen.UpdateRepoParams{
		OrgID: r.OrgID, ID: r.ID, Name: r.Name, Url: r.URL, Branch: r.Branch,
		Subpath: r.Subpath, CredentialID: db.NullUUID(r.CredentialID),
		VaultCredentialID: db.NullUUID(r.VaultCredentialID),
		SshHostKey:        hostKey, SshHostPublicKey: hostPub,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return Repo{}, ErrNameTaken
		}
		return Repo{}, mapErr(err)
	}
	return toRepo(row), nil
}

// ClearHostKey implements Store (see the interface's own comment).
func (s *SQLStore) ClearHostKey(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.UnpinRepoHostKey(ctx, gen.UnpinRepoHostKeyParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("unpin repo host key: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteRepo(ctx, gen.DeleteRepoParams{OrgID: orgID, ID: id})
	if err != nil {
		if db.IsForeignKeyViolation(err) {
			// The playbooks (org_id, repo_id) foreign key has no cascade on
			// purpose: a repository still referenced by a playbook is in use.
			return ErrInUse
		}
		return fmt.Errorf("delete repo: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceRepoVaultIdentities replaces a repository's labelled vault
// credential set: delete then insert
// (internal/db/queries/vault_identities.sql's own comment explains why
// never one data-modifying WITH), the two run inside one transaction —
// alertroutes.Store.Create's own posture, and its own reason: s.q runs each
// statement on the pool directly, auto-committing as it goes, so without a
// transaction a DELETE that succeeds followed by an INSERT that fails (a
// duplicate label in the submitted set, which this table's own missing ON
// CONFLICT turns into a unique violation — see the query file's comment)
// would leave the row already committed gone and nothing in its place. That
// is not a lost row the way the CTE mistake this file's INSERT comment
// warns about was; it is the repository's or template's entire vault
// identity set wiped to empty — a silently-ineffective state, same shape as
// the one alertroutes.Store.Create's own comment names, just reached by a
// different path (two auto-committing statements instead of one CTE whose
// internal order is undefined). An empty or nil slice is the legal "no
// vault passwords" result, not an error.
func (s *SQLStore) ReplaceRepoVaultIdentities(ctx context.Context, orgID, repoID uuid.UUID, identities []VaultIdentity) error {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace repo vault identities: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	if err := q.DeleteRepoVaultCredentials(ctx, gen.DeleteRepoVaultCredentialsParams{OrgID: orgID, RepoID: repoID}); err != nil {
		return fmt.Errorf("delete repo vault credentials: %w", err)
	}
	credentialIDs, labels := vaultIdentityArrays(identities)
	if err := q.InsertRepoVaultCredentials(ctx, gen.InsertRepoVaultCredentialsParams{
		OrgID: orgID, RepoID: repoID, CredentialIds: credentialIDs, Labels: labels,
	}); err != nil {
		return fmt.Errorf("insert repo vault credentials: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace repo vault identities: %w", err)
	}
	return nil
}

// ListRepoVaultIdentities returns a repository's labelled vault credential
// set, in label order. FromTemplate is always false: this table never
// carries a template's own identities — job_template_vault_credentials, a
// separate table, does.
func (s *SQLStore) ListRepoVaultIdentities(ctx context.Context, orgID, repoID uuid.UUID) ([]VaultIdentity, error) {
	rows, err := s.q.ListRepoVaultCredentials(ctx, gen.ListRepoVaultCredentialsParams{OrgID: orgID, RepoID: repoID})
	if err != nil {
		return nil, fmt.Errorf("list repo vault credentials: %w", err)
	}
	out := make([]VaultIdentity, len(rows))
	for i, r := range rows {
		out[i] = VaultIdentity{CredentialID: r.CredentialID, Label: r.Label}
	}
	return out, nil
}

// ListOrgRepoVaultIdentities is ListRepoVaultIdentities for the whole
// organization in one query (backlog 2.94), grouped by repo id; a
// repository absent from the result has no vault passwords, the same as
// ListRepoVaultIdentities returning an empty slice for it.
func (s *SQLStore) ListOrgRepoVaultIdentities(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]VaultIdentity, error) {
	rows, err := s.q.ListOrgRepoVaultCredentials(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list org repo vault credentials: %w", err)
	}
	out := map[uuid.UUID][]VaultIdentity{}
	for _, r := range rows {
		out[r.RepoID] = append(out[r.RepoID], VaultIdentity{CredentialID: r.CredentialID, Label: r.Label})
	}
	return out, nil
}

// vaultIdentityArrays splits a slice of VaultIdentity into the two parallel
// arrays InsertRepoVaultCredentials needs — built from the same slice in
// one pass, so the two arrays are always the same length and the query's
// lockstep unnest (InsertCheckItems' own shape, items.sql) never pads with
// NULLs.
func vaultIdentityArrays(identities []VaultIdentity) ([]uuid.UUID, []string) {
	credentialIDs := make([]uuid.UUID, len(identities))
	labels := make([]string, len(identities))
	for i, id := range identities {
		credentialIDs[i] = id.CredentialID
		labels[i] = id.Label
	}
	return credentialIDs, labels
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func toRepo(r gen.GitRepo) Repo {
	return Repo{
		ID: r.ID, OrgID: r.OrgID, Name: r.Name, URL: r.Url, Branch: r.Branch, Subpath: r.Subpath,
		SharedRepoID:      db.UUIDPtr(r.SharedRepoID),
		CredentialID:      db.UUIDPtr(r.CredentialID),
		VaultCredentialID: db.UUIDPtr(r.VaultCredentialID),
		SSHHostKey:        textOrEmpty(r.SshHostKey), SSHHostPublicKey: textOrEmpty(r.SshHostPublicKey),
		LastSync: db.TimePtr(r.LastSync), LastSyncError: textOrEmpty(r.LastSyncError),
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// textOrEmpty dereferences an optional text column; the sync result store
// maps NULL and "" to the same "no failure" state.
func textOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
