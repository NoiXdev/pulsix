package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/varsource"
)

// LockVarSources implements Store: the repository row, locked until the
// caller's transaction ends (queries/var_sources.sql explains the lock).
// ErrNotFound when the repository is gone.
func (s *SQLStore) LockVarSources(ctx context.Context, orgID, repoID uuid.UUID) error {
	if _, err := s.q.LockRepoForVarSources(ctx, gen.LockRepoForVarSourcesParams{OrgID: orgID, RepoID: repoID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock repository for variable sources: %w", err)
	}
	return nil
}

// ReplaceVarSources implements Store: the repository's record and its
// playbook rows are replaced in one transaction (a savepoint inside the
// job finish's own). ErrNotFound when the repository is gone; nothing is
// written then.
func (s *SQLStore) ReplaceVarSources(ctx context.Context, orgID, repoID uuid.UUID, rec SourcesRecord, playbooks []varsource.Playbook) error {
	leftOut, err := json.Marshal(nonNil(rec.LeftOut))
	if err != nil {
		return fmt.Errorf("encode left-out playbooks: %w", err)
	}
	notes, err := json.Marshal(nonNil(rec.Notes))
	if err != nil {
		return fmt.Errorf("encode variable source notes: %w", err)
	}
	results := make([][]byte, len(playbooks))
	for i, pb := range playbooks {
		if results[i], err = json.Marshal(pb); err != nil {
			return fmt.Errorf("encode playbook source %q: %w", pb.Path, err)
		}
	}

	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin replace variable sources: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)

	n, err := q.UpsertRepoVarSources(ctx, gen.UpsertRepoVarSourcesParams{
		CommitSha: rec.Commit, SyncedAt: rec.SyncedAt, Incomplete: rec.Incomplete,
		LeftOut: leftOut, Notes: notes, OrgID: orgID, RepoID: repoID,
	})
	if err != nil {
		return fmt.Errorf("upsert repo variable sources: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := q.DeleteRepoPlaybookSources(ctx, gen.DeleteRepoPlaybookSourcesParams{OrgID: orgID, RepoID: repoID}); err != nil {
		return fmt.Errorf("delete repo playbook sources: %w", err)
	}
	for i, pb := range playbooks {
		if err := q.InsertRepoPlaybookSource(ctx, gen.InsertRepoPlaybookSourceParams{
			OrgID: orgID, RepoID: repoID, Path: pb.Path, Result: results[i],
		}); err != nil {
			return fmt.Errorf("insert repo playbook source %q: %w", pb.Path, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit replace variable sources: %w", err)
	}
	return nil
}

// MarkVarSourcesUnread implements Store. A repository gone meanwhile is a
// no-op.
func (s *SQLStore) MarkVarSourcesUnread(ctx context.Context, orgID, repoID uuid.UUID, at time.Time) error {
	if err := s.q.MarkRepoVarSourcesUnread(ctx, gen.MarkRepoVarSourcesUnreadParams{SyncedAt: at, OrgID: orgID, RepoID: repoID}); err != nil {
		return fmt.Errorf("mark repo variable sources unread: %w", err)
	}
	return nil
}

// GetVarSources implements Store; ErrNotFound when no sync recorded any.
func (s *SQLStore) GetVarSources(ctx context.Context, orgID, repoID uuid.UUID) (SourcesRecord, error) {
	row, err := s.q.GetRepoVarSources(ctx, gen.GetRepoVarSourcesParams{OrgID: orgID, RepoID: repoID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SourcesRecord{}, ErrNotFound
		}
		return SourcesRecord{}, fmt.Errorf("get repo variable sources: %w", err)
	}
	rec := SourcesRecord{
		Commit: row.CommitSha, SyncedAt: row.SyncedAt, WorkerReads: row.WorkerReads, Incomplete: row.Incomplete,
	}
	if err := json.Unmarshal(row.LeftOut, &rec.LeftOut); err != nil {
		return SourcesRecord{}, fmt.Errorf("decode left-out playbooks: %w", err)
	}
	if err := json.Unmarshal(row.Notes, &rec.Notes); err != nil {
		return SourcesRecord{}, fmt.Errorf("decode variable source notes: %w", err)
	}
	return rec, nil
}

// ListPlaybookSources implements Store: every playbook row, by path.
func (s *SQLStore) ListPlaybookSources(ctx context.Context, orgID, repoID uuid.UUID) ([]varsource.Playbook, error) {
	rows, err := s.q.ListRepoPlaybookSources(ctx, gen.ListRepoPlaybookSourcesParams{OrgID: orgID, RepoID: repoID})
	if err != nil {
		return nil, fmt.Errorf("list repo playbook sources: %w", err)
	}
	out := make([]varsource.Playbook, len(rows))
	for i, r := range rows {
		if out[i], err = decodePlaybookSource(r.Path, r.Result); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetPlaybookSource implements Store; ErrNotFound when the path has no row.
func (s *SQLStore) GetPlaybookSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (varsource.Playbook, error) {
	row, err := s.q.GetRepoPlaybookSource(ctx, gen.GetRepoPlaybookSourceParams{OrgID: orgID, RepoID: repoID, Path: path})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return varsource.Playbook{}, ErrNotFound
		}
		return varsource.Playbook{}, fmt.Errorf("get repo playbook source: %w", err)
	}
	return decodePlaybookSource(row.Path, row.Result)
}

// decodePlaybookSource is one stored parse result; the row's path is the
// playbook's.
func decodePlaybookSource(path string, result []byte) (varsource.Playbook, error) {
	var pb varsource.Playbook
	if err := json.Unmarshal(result, &pb); err != nil {
		return varsource.Playbook{}, fmt.Errorf("decode playbook source %q: %w", path, err)
	}
	pb.Path = path
	return pb, nil
}

// nonNil stores an absent list as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
