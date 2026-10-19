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
)

// SQLStore implements Store with sqlc queries on *db.DB, so every statement
// runs inside the request's organization scope.
type SQLStore struct{ q *gen.Queries }

// NewSQLStore builds the store.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{q: gen.New(d)} }

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
// to carry, and importing internal/repos just to reach that one field would
// put the dependency edge the wrong way round for a single struct. The
// caller that resolves a run's identities already imports both packages
// (internal/repos.ResolveVaultIdentities's own call site) and builds
// repos.VaultIdentity{..., FromTemplate: true} from this type itself.
type VaultCredential struct {
	CredentialID uuid.UUID
	Label        string
}

// ReplaceVaultCredentials replaces a template's labelled vault credential
// set in one call: delete then insert
// (internal/db/queries/vault_identities.sql's own comment explains why
// never one data-modifying WITH). An empty or nil slice is the legal "no
// added vault passwords" result, not an error.
func (s *SQLStore) ReplaceVaultCredentials(ctx context.Context, orgID, templateID uuid.UUID, items []VaultCredential) error {
	if err := s.q.DeleteTemplateVaultCredentials(ctx, gen.DeleteTemplateVaultCredentialsParams{OrgID: orgID, TemplateID: templateID}); err != nil {
		return fmt.Errorf("delete template vault credentials: %w", err)
	}
	credentialIDs := make([]uuid.UUID, len(items))
	labels := make([]string, len(items))
	for i, it := range items {
		credentialIDs[i] = it.CredentialID
		labels[i] = it.Label
	}
	if err := s.q.InsertTemplateVaultCredentials(ctx, gen.InsertTemplateVaultCredentialsParams{
		OrgID: orgID, TemplateID: templateID, CredentialIds: credentialIDs, Labels: labels,
	}); err != nil {
		return fmt.Errorf("insert template vault credentials: %w", err)
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
