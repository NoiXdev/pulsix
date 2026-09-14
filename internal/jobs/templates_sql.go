package jobs

import (
	"context"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// SQLTemplates implements TemplateSource with the one template read the
// enqueue and claim paths need, joined with its playbook. The templates
// service (Task 7) owns the full CRUD surface on the same table.
type SQLTemplates struct{ q *gen.Queries }

// NewSQLTemplates builds the source on d itself, never on its raw pool.
func NewSQLTemplates(d *db.DB) *SQLTemplates { return &SQLTemplates{q: gen.New(d)} }

func (s *SQLTemplates) TemplateLookup(ctx context.Context, orgID, id uuid.UUID) (TemplateRow, error) {
	row, err := s.q.GetJobTemplateForRun(ctx, gen.GetJobTemplateForRunParams{OrgID: orgID, ID: id})
	if err != nil {
		return TemplateRow{}, mapErr(err)
	}
	return TemplateRow{
		ID:              row.ID,
		OrgID:           row.OrgID,
		PlaybookID:      row.PlaybookID,
		PlaybookRepo:    db.UUIDPtr(row.PlaybookRepoID),
		PlaybookPath:    textVal(row.PlaybookPath),
		PlaybookInline:  textVal(row.PlaybookInline),
		Scope:           row.Scope,
		Limit:           row.Limit,
		ExtraVars:       row.ExtraVars,
		ExtraVarsSchema: row.ExtraVarsSchema,
		CredentialID:    db.UUIDPtr(row.CredentialID),
	}, nil
}

// textVal dereferences an optional text column; the playbooks CHECK
// constraint guarantees the field matching the source is set, so nil only
// means "the other source".
func textVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
