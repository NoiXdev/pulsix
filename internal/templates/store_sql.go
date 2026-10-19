package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope. d is kept alongside q (not
// just q built once over d) so ReplaceVaultCredentials can open its own
// transaction — every other method here is a single statement and has no
// need of one.
type SQLStore struct {
	q *gen.Queries
	d *db.DB
}

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d), d: d} }

func (s *SQLStore) Create(ctx context.Context, t Template) (Template, error) {
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return Template{}, fmt.Errorf("encode template scope: %w", err)
	}
	vars, err := json.Marshal(t.ExtraVars)
	if err != nil {
		return Template{}, fmt.Errorf("encode template extra vars: %w", err)
	}
	var schemaRaw []byte
	if t.ExtraVarsSchema != nil {
		if schemaRaw, err = json.Marshal(*t.ExtraVarsSchema); err != nil {
			return Template{}, fmt.Errorf("encode template extra vars schema: %w", err)
		}
	}
	row, err := s.q.InsertTemplate(ctx, gen.InsertTemplateParams{
		ID: t.ID, OrgID: t.OrgID, Name: t.Name, PlaybookID: t.PlaybookID,
		Scope: scope, Limit: t.Limit, ExtraVars: vars,
		ExtraVarsSchema: schemaRaw, CredentialID: db.NullUUID(t.CredentialID),
	})
	if err != nil {
		return Template{}, mapWriteErr(err, "create template")
	}
	return toTemplate(row)
}

func (s *SQLStore) Get(ctx context.Context, orgID, id uuid.UUID) (Template, error) {
	row, err := s.q.GetTemplate(ctx, gen.GetTemplateParams{OrgID: orgID, ID: id})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Template{}, ErrNotFound
		}
		return Template{}, fmt.Errorf("get template: %w", err)
	}
	return toTemplate(row)
}

func (s *SQLStore) List(ctx context.Context, orgID uuid.UUID) ([]Template, error) {
	rows, err := s.q.ListTemplates(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	out := make([]Template, len(rows))
	for i, r := range rows {
		if out[i], err = toTemplate(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLStore) Update(ctx context.Context, t Template) (Template, error) {
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return Template{}, fmt.Errorf("encode template scope: %w", err)
	}
	vars, err := json.Marshal(t.ExtraVars)
	if err != nil {
		return Template{}, fmt.Errorf("encode template extra vars: %w", err)
	}
	var schemaRaw []byte
	if t.ExtraVarsSchema != nil {
		if schemaRaw, err = json.Marshal(*t.ExtraVarsSchema); err != nil {
			return Template{}, fmt.Errorf("encode template extra vars schema: %w", err)
		}
	}
	row, err := s.q.UpdateTemplate(ctx, gen.UpdateTemplateParams{
		OrgID: t.OrgID, ID: t.ID, Name: t.Name, PlaybookID: t.PlaybookID,
		Scope: scope, Limit: t.Limit, ExtraVars: vars,
		ExtraVarsSchema: schemaRaw, CredentialID: db.NullUUID(t.CredentialID),
	})
	if err != nil {
		return Template{}, mapWriteErr(err, "update template")
	}
	return toTemplate(row)
}

func (s *SQLStore) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.q.DeleteTemplate(ctx, gen.DeleteTemplateParams{OrgID: orgID, ID: id})
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// VaultCredential is one labelled vault password a job template adds
// (migration 00059's second table, spec decision 1 — a template may add
// passwords a shared repository's other templates must not have). Local to
// this package rather than internal/repos.VaultIdentity: every row this
// store reads or writes IS from a template, so there is no "FromTemplate"
// field to carry on the struct itself — ToVaultIdentity below sets it.
type VaultCredential struct {
	CredentialID uuid.UUID
	Label        string
}

// ToVaultIdentity converts one of a template's own vault credentials into
// the shape internal/repos.ResolveVaultIdentities needs for its template
// argument, with FromTemplate forced true. One named place for this
// conversion, so a future caller (Task 4's run assembly) calls this once
// per row from ListVaultCredentials rather than rebuilding the struct
// literal by hand at each call site, where the two packages' fields could
// drift apart unnoticed.
//
// This does make internal/templates import internal/repos directly, which
// is exactly the signal the plan's pre-Task-2 ruling said to report: "if
// internal/templates ends up importing repos solely to reach this type,
// say so — that is the signal the placement is wrong." Reported, but the
// reverse placement is not available to fix it: internal/templates already
// depends on internal/repos transitively through internal/playbooks
// (templates -> playbooks -> repos, playbooks.RepoSource), so this adds a
// direct edge where a transitive one already existed rather than a new
// cycle. The opposite direction — repos importing templates so this method
// could live there instead — is not a style choice available to take:
// repos already sits underneath playbooks underneath templates, so
// internal/repos importing internal/templates would be an import cycle,
// not merely an undesirable edge.
func (c VaultCredential) ToVaultIdentity() repos.VaultIdentity {
	return repos.VaultIdentity{CredentialID: c.CredentialID, Label: c.Label, FromTemplate: true}
}

// ReplaceVaultCredentials replaces a template's labelled vault credential
// set: delete then insert (internal/db/queries/vault_identities.sql's own
// comment explains why never one data-modifying WITH), the two run inside
// one transaction — repos.SQLStore.ReplaceRepoVaultIdentities' own posture
// and its own reason: s.q runs each statement on the pool directly,
// auto-committing as it goes, so without a transaction an INSERT that fails
// after the DELETE already committed would wipe the template's whole set to
// empty rather than leaving it unchanged. An empty or nil slice is the
// legal "no added vault passwords" result, not an error.
func (s *SQLStore) ReplaceVaultCredentials(ctx context.Context, orgID, templateID uuid.UUID, items []VaultCredential) error {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace template vault credentials: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	if err := q.DeleteTemplateVaultCredentials(ctx, gen.DeleteTemplateVaultCredentialsParams{OrgID: orgID, TemplateID: templateID}); err != nil {
		return fmt.Errorf("delete template vault credentials: %w", err)
	}
	credentialIDs := make([]uuid.UUID, len(items))
	labels := make([]string, len(items))
	for i, it := range items {
		credentialIDs[i] = it.CredentialID
		labels[i] = it.Label
	}
	if err := q.InsertTemplateVaultCredentials(ctx, gen.InsertTemplateVaultCredentialsParams{
		OrgID: orgID, TemplateID: templateID, CredentialIds: credentialIDs, Labels: labels,
	}); err != nil {
		return fmt.Errorf("insert template vault credentials: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace template vault credentials: %w", err)
	}
	return nil
}

// ListVaultCredentials returns a template's own labelled vault credential
// set, in label order — the identities it adds on top of its repository's,
// never the repository's own (repos.SQLStore.ListRepoVaultIdentities reads
// those, from the other table).
func (s *SQLStore) ListVaultCredentials(ctx context.Context, orgID, templateID uuid.UUID) ([]VaultCredential, error) {
	rows, err := s.q.ListTemplateVaultCredentials(ctx, gen.ListTemplateVaultCredentialsParams{OrgID: orgID, TemplateID: templateID})
	if err != nil {
		return nil, fmt.Errorf("list template vault credentials: %w", err)
	}
	out := make([]VaultCredential, len(rows))
	for i, r := range rows {
		out[i] = VaultCredential{CredentialID: r.CredentialID, Label: r.Label}
	}
	return out, nil
}

// mapWriteErr maps the unique (org_id, name) constraint to ErrNameTaken and
// the composite foreign keys to ErrNotFound (a playbook or credential
// deleted between the service's check and this write — the same "invisible,
// not forbidden" answer a pre-check would give), and wraps everything else.
func mapWriteErr(err error, op string) error {
	switch {
	case db.IsUniqueViolation(err):
		return ErrNameTaken
	case db.IsForeignKeyViolation(err):
		return ErrNotFound
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

// toTemplate decodes a row. An empty or NULL scope jsonb decodes to the
// zero Scope — the whole organization; NULL extra_vars_schema decodes to a
// nil *schema.Schema.
func toTemplate(row gen.JobTemplate) (Template, error) {
	t := Template{
		ID: row.ID, OrgID: row.OrgID, Name: row.Name, PlaybookID: row.PlaybookID,
		Limit: row.Limit, CredentialID: db.UUIDPtr(row.CredentialID),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if len(row.Scope) > 0 {
		if err := json.Unmarshal(row.Scope, &t.Scope); err != nil {
			return Template{}, fmt.Errorf("decode template scope: %w", err)
		}
	}
	if len(row.ExtraVars) > 0 {
		if err := json.Unmarshal(row.ExtraVars, &t.ExtraVars); err != nil {
			return Template{}, fmt.Errorf("decode template extra vars: %w", err)
		}
	}
	if t.ExtraVars == nil {
		t.ExtraVars = map[string]any{}
	}
	if len(row.ExtraVarsSchema) > 0 {
		sc := schema.Schema{}
		if err := json.Unmarshal(row.ExtraVarsSchema, &sc); err != nil {
			return Template{}, fmt.Errorf("decode template extra vars schema: %w", err)
		}
		t.ExtraVarsSchema = &sc
	}
	return t, nil
}
