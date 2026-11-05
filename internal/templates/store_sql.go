package templates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

// InTx implements Store: one organization-scoped write transaction that
// every store call made with the context it passes to fn joins, since
// this store's *db.DB picks a bound transaction up from the context
// (db.WithWriteTx). Nested use is not supported — db.beginOrg always
// begins a fresh pool transaction rather than joining one already bound.
func (s *SQLStore) InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error {
	return s.d.WithWriteTx(ctx, orgID, fn)
}

func (s *SQLStore) Create(ctx context.Context, t Template) (Template, error) {
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return Template{}, fmt.Errorf("encode template scope: %w", err)
	}
	vars, schemaRaw, err := encodeVars(t)
	if err != nil {
		return Template{}, err
	}
	row, err := s.q.InsertTemplate(ctx, gen.InsertTemplateParams{
		ID: t.ID, OrgID: t.OrgID, Name: t.Name, PlaybookID: nullablePlaybookID(t.PlaybookID),
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

// Update implements Store: the template's editable fields, guarded by
// ifRevision when it is given.
func (s *SQLStore) Update(ctx context.Context, t Template, ifRevision *int64) (Template, error) {
	scope, err := json.Marshal(t.Scope)
	if err != nil {
		return Template{}, fmt.Errorf("encode template scope: %w", err)
	}
	vars, schemaRaw, err := encodeVars(t)
	if err != nil {
		return Template{}, err
	}
	row, err := s.q.UpdateTemplate(ctx, gen.UpdateTemplateParams{
		OrgID: t.OrgID, ID: t.ID, Name: t.Name, PlaybookID: nullablePlaybookID(t.PlaybookID),
		Scope: scope, Lim: t.Limit, ExtraVars: vars,
		ExtraVarsSchema: schemaRaw, CredentialID: db.NullUUID(t.CredentialID),
		IfRevision: ifRevision,
	})
	if err != nil {
		return Template{}, s.guardedWriteErr(ctx, err, t, "update template")
	}
	return toTemplate(row)
}

// UpdateVariables implements Store: the schema, the values and the
// variables source columns, guarded by ifRevision when it is given.
func (s *SQLStore) UpdateVariables(ctx context.Context, t Template, ifRevision *int64) (Template, error) {
	vars, schemaRaw, err := encodeVars(t)
	if err != nil {
		return Template{}, err
	}
	mode := t.VarsMode
	if mode == "" {
		mode = ModeOff
	}
	row, err := s.q.UpdateTemplateVariables(ctx, gen.UpdateTemplateVariablesParams{
		OrgID: t.OrgID, ID: t.ID, ExtraVars: vars, ExtraVarsSchema: schemaRaw,
		VarsMode: string(mode), VarsSourceRev: t.VarsSourceRev, VarsAppliedAt: db.NullTime(t.VarsAppliedAt),
		IfRevision: ifRevision,
	})
	if err != nil {
		return Template{}, s.guardedWriteErr(ctx, err, t, "update template variables")
	}
	return toTemplate(row)
}

// guardedWriteErr maps a revision-guarded write's error: no row back is a
// stale revision when the template exists (ErrRevisionMismatch) and
// ErrNotFound when it does not; everything else goes through mapWriteErr.
func (s *SQLStore) guardedWriteErr(ctx context.Context, err error, t Template, op string) error {
	if !errors.Is(err, pgx.ErrNoRows) {
		return mapWriteErr(err, op)
	}
	if _, gerr := s.Get(ctx, t.OrgID, t.ID); gerr != nil {
		return gerr
	}
	return ErrRevisionMismatch
}

// encodeVars encodes a template's extra vars and its schema (nil when the
// template carries none) for the jsonb columns.
func encodeVars(t Template) (vars, schemaRaw []byte, err error) {
	if vars, err = json.Marshal(t.ExtraVars); err != nil {
		return nil, nil, fmt.Errorf("encode template extra vars: %w", err)
	}
	if t.ExtraVarsSchema != nil {
		if schemaRaw, err = json.Marshal(*t.ExtraVarsSchema); err != nil {
			return nil, nil, fmt.Errorf("encode template extra vars schema: %w", err)
		}
	}
	return vars, schemaRaw, nil
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

// ListOrgVaultCredentials is ListVaultCredentials for every template of
// the organization in one query (backlog 2.94), grouped by template id;
// Service.List otherwise called ListVaultCredentials once per row it was
// about to return. A template absent from the result has no added vault
// passwords, the same as ListVaultCredentials returning an empty slice.
func (s *SQLStore) ListOrgVaultCredentials(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]VaultCredential, error) {
	rows, err := s.q.ListOrgTemplateVaultCredentials(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list org template vault credentials: %w", err)
	}
	out := map[uuid.UUID][]VaultCredential{}
	for _, r := range rows {
		out[r.TemplateID] = append(out[r.TemplateID], VaultCredential{CredentialID: r.CredentialID, Label: r.Label})
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

// nullablePlaybookID converts the domain's uuid.Nil-means-unresolved
// convention (Template.PlaybookID's own doc comment) to the nullable
// column migration 00061 made of job_templates.playbook_id. An ordinary
// template's id is never uuid.Nil — Service.build always resolves one
// through playbooks.Get before a Create or Update reaches this store — so
// this only ever produces an absent value for a shared template
// projection whose organization has not synced the definition's playbook
// path yet.
func nullablePlaybookID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return db.NullUUID(&id)
}

// toTemplate decodes a row. An empty or NULL scope jsonb decodes to the
// zero Scope — the whole organization; NULL extra_vars_schema decodes to a
// nil *schema.Schema; NULL playbook_id decodes to uuid.Nil (Template.
// PlaybookID's own doc comment) and NULL shared_template_id to a nil
// *uuid.UUID (an ordinary, non-shared template).
func toTemplate(row gen.JobTemplate) (Template, error) {
	playbookID := uuid.Nil
	if p := db.UUIDPtr(row.PlaybookID); p != nil {
		playbookID = *p
	}
	t := Template{
		ID: row.ID, OrgID: row.OrgID, Name: row.Name, PlaybookID: playbookID,
		SharedTemplateID: db.UUIDPtr(row.SharedTemplateID),
		Limit:            row.Limit, CredentialID: db.UUIDPtr(row.CredentialID),
		Revision: row.Revision, VarsMode: SourceMode(row.VarsMode), VarsSourceRev: row.VarsSourceRev,
		VarsAppliedAt: db.TimePtr(row.VarsAppliedAt),
		CreatedAt:     row.CreatedAt, UpdatedAt: row.UpdatedAt,
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
