package templates

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// The SQL half of VarsStore (varsource.go): the variables link's writes
// and the automatic apply's lists.

// UpdateVarsSource implements Store: the schema and the variables source
// columns, never the values, guarded by ifRevision and ifSourceRev when
// they are given.
func (s *SQLStore) UpdateVarsSource(ctx context.Context, t Template, ifRevision *int64, ifSourceRev *string) (Template, error) {
	_, schemaRaw, err := encodeVars(t)
	if err != nil {
		return Template{}, err
	}
	row, err := s.q.UpdateTemplateVarsSource(ctx, gen.UpdateTemplateVarsSourceParams{
		OrgID: t.OrgID, ID: t.ID, ExtraVarsSchema: schemaRaw, VarsMode: string(storedMode(t.VarsMode)),
		VarsSourceRev: t.VarsSourceRev, VarsAppliedAt: db.NullTime(t.VarsAppliedAt), IfRevision: ifRevision, IfSourceRev: ifSourceRev,
	})
	if err != nil {
		return Template{}, s.guardedWriteErr(ctx, err, t, "update template variables source")
	}
	return toTemplate(row)
}

// RecordSourceRev implements Store: the applied source revision and its
// time, without moving the revision.
func (s *SQLStore) RecordSourceRev(ctx context.Context, t Template, ifRevision int64, ifSourceRev string) (Template, error) {
	row, err := s.q.RecordTemplateSourceRev(ctx, gen.RecordTemplateSourceRevParams{
		OrgID: t.OrgID, ID: t.ID, VarsSourceRev: t.VarsSourceRev, VarsAppliedAt: db.NullTime(t.VarsAppliedAt),
		IfRevision: ifRevision, IfSourceRev: ifSourceRev,
	})
	if err != nil {
		return Template{}, s.guardedWriteErr(ctx, err, t, "record template source revision")
	}
	return toTemplate(row)
}

// ListAutomaticForRepo implements Store.
func (s *SQLStore) ListAutomaticForRepo(ctx context.Context, orgID, repoID uuid.UUID) ([]Template, error) {
	rows, err := s.q.ListAutomaticTemplatesForRepo(ctx, gen.ListAutomaticTemplatesForRepoParams{OrgID: orgID, RepoID: db.NullUUID(&repoID)})
	if err != nil {
		return nil, fmt.Errorf("list automatic templates of a repository: %w", err)
	}
	return toTemplates(rows)
}

// ListAutomaticForPlaybook implements Store.
func (s *SQLStore) ListAutomaticForPlaybook(ctx context.Context, orgID, playbookID uuid.UUID) ([]Template, error) {
	rows, err := s.q.ListAutomaticTemplatesForPlaybook(ctx, gen.ListAutomaticTemplatesForPlaybookParams{OrgID: orgID, PlaybookID: db.NullUUID(&playbookID)})
	if err != nil {
		return nil, fmt.Errorf("list automatic templates of a playbook: %w", err)
	}
	return toTemplates(rows)
}
