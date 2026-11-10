package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/varsource"
)

// SetLogger sets where the store reports a stored playbook row it skips
// because it no longer decodes; slog.Default without.
func (s *SQLStore) SetLogger(l *slog.Logger) { s.log = l }

func (s *SQLStore) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

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
	return decodeSourcesRecord(row.CommitSha, row.SyncedAt, row.WorkerReads, row.Incomplete, row.LeftOut, row.Notes)
}

// GetPlaybookVarSource implements Store: the record and the row of one
// playbook path, read in one statement. ErrNotFound when no sync recorded
// any; a nil playbook when the record has no row for the path, or its row
// no longer decodes (decodePlaybookSource).
func (s *SQLStore) GetPlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (SourcesRecord, *varsource.Playbook, error) {
	row, err := s.q.GetPlaybookVarSource(ctx, gen.GetPlaybookVarSourceParams{Path: path, OrgID: orgID, RepoID: repoID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SourcesRecord{}, nil, ErrNotFound
		}
		return SourcesRecord{}, nil, fmt.Errorf("get playbook variable source: %w", err)
	}
	rec, err := decodeSourcesRecord(row.CommitSha, row.SyncedAt, row.WorkerReads, row.Incomplete, row.LeftOut, row.Notes)
	if err != nil {
		return SourcesRecord{}, nil, err
	}
	if row.Path == nil {
		return rec, nil, nil
	}
	pb, ok := s.decodePlaybookSource(ctx, orgID, repoID, *row.Path, row.Result)
	if !ok {
		return rec, nil, nil
	}
	return rec, &pb, nil
}

// decodeSourcesRecord is a stored record's columns as a SourcesRecord.
func decodeSourcesRecord(commit string, syncedAt time.Time, workerReads, incomplete bool, leftOut, notes []byte) (SourcesRecord, error) {
	rec := SourcesRecord{Commit: commit, SyncedAt: syncedAt, WorkerReads: workerReads, Incomplete: incomplete}
	if err := json.Unmarshal(leftOut, &rec.LeftOut); err != nil {
		return SourcesRecord{}, fmt.Errorf("decode left-out playbooks: %w", err)
	}
	if err := json.Unmarshal(notes, &rec.Notes); err != nil {
		return SourcesRecord{}, fmt.Errorf("decode variable source notes: %w", err)
	}
	return rec, nil
}

// ListPlaybookSources implements Store: every playbook row, by path. A row
// that no longer decodes is left out (decodePlaybookSource).
func (s *SQLStore) ListPlaybookSources(ctx context.Context, orgID, repoID uuid.UUID) ([]varsource.Playbook, error) {
	rows, err := s.q.ListRepoPlaybookSources(ctx, gen.ListRepoPlaybookSourcesParams{OrgID: orgID, RepoID: repoID})
	if err != nil {
		return nil, fmt.Errorf("list repo playbook sources: %w", err)
	}
	out := make([]varsource.Playbook, 0, len(rows))
	for _, r := range rows {
		if pb, ok := s.decodePlaybookSource(ctx, orgID, repoID, r.Path, r.Result); ok {
			out = append(out, pb)
		}
	}
	return out, nil
}

// maxStoredResultReadBytes is the longest stored playbook result a read
// decodes (backlog 7.122), like playbooks.maxInlineReadBytes for editor
// playbooks. A result is stored within varsource.MaxReportBytes, the
// cap on the compact JSON a worker sends, but jsonb renders it back
// differently: a space after every ':' and ',' (at most one byte per
// two), string escapes as the character itself (never longer), and a
// number as the numeric it stored, written in full. decodeReport refuses
// a report holding a number Go writes in exponent form, so the read-back
// text is at most about one and a half times the stored one, and a
// result stored within the cap still reads. A longer one (a row written
// before that check, by a worker that did not apply it) is not decoded.
const maxStoredResultReadBytes = 2 * varsource.MaxReportBytes

// decodePlaybookSource is one stored parse result; the row's path is the
// playbook's. A row that no longer decodes (written by another version),
// or is longer than maxStoredResultReadBytes, is treated as missing, with
// a log line: failing instead would fail every later sync finish of the
// repository, which reads the previous rows.
func (s *SQLStore) decodePlaybookSource(ctx context.Context, orgID, repoID uuid.UUID, path string, result []byte) (varsource.Playbook, bool) {
	if len(result) > maxStoredResultReadBytes {
		s.logger().WarnContext(ctx, "stored playbook variable sources too large to read; treated as missing",
			"org", orgID, "repo", repoID, "path", path, "bytes", len(result), "limit", maxStoredResultReadBytes)
		return varsource.Playbook{}, false
	}
	var pb varsource.Playbook
	if err := json.Unmarshal(result, &pb); err != nil {
		s.logger().WarnContext(ctx, "stored playbook variable sources do not decode; treated as missing",
			"org", orgID, "repo", repoID, "path", path, "err", err)
		return varsource.Playbook{}, false
	}
	pb.Path = path
	return pb, true
}

// nonNil stores an absent list as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
