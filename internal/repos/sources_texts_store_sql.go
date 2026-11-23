package repos

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// ListPlaybookTexts implements Store: every playbook row's text, by path.
func (s *SQLStore) ListPlaybookTexts(ctx context.Context, orgID, repoID uuid.UUID) (map[string]PlaybookText, error) {
	rows, err := s.q.ListRepoPlaybookTexts(ctx, gen.ListRepoPlaybookTextsParams{OrgID: orgID, RepoID: repoID})
	if err != nil {
		return nil, fmt.Errorf("list repo playbook texts: %w", err)
	}
	out := make(map[string]PlaybookText, len(rows))
	for _, r := range rows {
		out[r.Path] = PlaybookText{Content: r.Content, LeftOut: derefString(r.ContentLeftOut)}
	}
	return out, nil
}

// GetPlaybookText implements Store: one path's text and its record, read
// in one statement. ErrNotFound without a record; a record without a row
// for the path answers no text and no reason.
func (s *SQLStore) GetPlaybookText(ctx context.Context, orgID, repoID uuid.UUID, path string) (StoredPlaybookText, error) {
	row, err := s.q.GetRepoPlaybookText(ctx, gen.GetRepoPlaybookTextParams{OrgID: orgID, RepoID: repoID, Path: path})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredPlaybookText{}, ErrNotFound
		}
		return StoredPlaybookText{}, fmt.Errorf("get repo playbook text: %w", err)
	}
	return StoredPlaybookText{
		Commit: row.CommitSha, SyncedAt: row.SyncedAt, Kept: row.Kept,
		PlaybookText: PlaybookText{Content: row.Content, LeftOut: derefString(row.ContentLeftOut)},
	}, nil
}

// nullString stores "" as NULL.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
