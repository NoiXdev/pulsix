// The variable sources a repo sync read (backlog 7.75): the worker ships
// its varsource.Report with the sync result, the job queue hands it here
// inside the finish transaction (RecordVarSources), and the variables page
// and the apply read it back (VarSources, PlaybookVarSource).
package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/varsource"
)

// SourcesRecord is what the last sync of a repository read, apart from its
// playbooks. WorkerReads is false when the last sync came from a worker
// that reads no sources (one minor older): the record is the previous
// read, kept. Incomplete and LeftOut: a cap left the named playbooks
// unread; they keep their previous rows.
type SourcesRecord struct {
	Commit      string
	SyncedAt    time.Time
	WorkerReads bool
	Incomplete  bool
	LeftOut     []string
	Notes       []varsource.Note
}

// SetLogger sets where RecordVarSources reports what it skips (a
// malformed report, a repository gone meanwhile); slog.Default without.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l }

func (s *Service) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// RecordVarSources stores the variable sources report raw a successful
// sync of the repository sent, read at at. A nil raw is a sync by a worker
// that reads no sources: the previous record is kept and flagged.
//
// What the record becomes:
//   - a Failed report (nothing of the repository read) keeps the previous
//     commit and playbook rows and stores only its notes and at;
//   - otherwise the playbooks are replaced by the report's, except those
//     named in LeftOut, which keep their previous rows (a left-out name
//     without one gets none: it was never read);
//   - an empty commit (the worker could not read it) keeps the previous
//     commit, it is never a new revision.
//
// A report that is malformed, too large or would not store is logged and
// ignored, the record untouched, and so is a repository deleted meanwhile:
// neither may fail the sync. Only a store failure is returned.
func (s *Service) RecordVarSources(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, raw json.RawMessage) error {
	if raw == nil {
		return s.store.MarkVarSourcesUnread(ctx, orgID, repoID, at)
	}
	r, err := decodeReport(raw)
	if err != nil {
		s.logger().WarnContext(ctx, "variable sources ignored", "org", orgID, "repo", repoID, "err", err)
		return nil
	}
	rec, playbooks, err := s.nextSources(ctx, orgID, repoID, at, r)
	if err != nil {
		return err
	}
	if err := s.store.ReplaceVarSources(ctx, orgID, repoID, rec, playbooks); err != nil {
		if errors.Is(err, ErrNotFound) {
			s.logger().WarnContext(ctx, "variable sources skipped: repository no longer exists", "org", orgID, "repo", repoID)
			return nil
		}
		return fmt.Errorf("store variable sources: %w", err)
	}
	return nil
}

// nextSources is the record and playbook rows the report r leaves, given
// what is stored.
func (s *Service) nextSources(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, r varsource.Report) (SourcesRecord, []varsource.Playbook, error) {
	var prev SourcesRecord
	if r.Failed || r.Commit == "" {
		var err error
		if prev, err = s.store.GetVarSources(ctx, orgID, repoID); err != nil && !errors.Is(err, ErrNotFound) {
			return SourcesRecord{}, nil, fmt.Errorf("get previous variable sources: %w", err)
		}
	}
	var prevPlaybooks []varsource.Playbook
	if r.Failed || len(r.LeftOut) > 0 {
		var err error
		if prevPlaybooks, err = s.store.ListPlaybookSources(ctx, orgID, repoID); err != nil {
			return SourcesRecord{}, nil, fmt.Errorf("list previous playbook sources: %w", err)
		}
	}

	if r.Failed {
		return SourcesRecord{
			Commit: prev.Commit, SyncedAt: at, WorkerReads: true,
			Incomplete: prev.Incomplete, LeftOut: prev.LeftOut, Notes: r.Notes,
		}, prevPlaybooks, nil
	}
	commit := r.Commit
	if commit == "" {
		commit = prev.Commit
	}
	playbooks := r.Playbooks
	if len(r.LeftOut) > 0 {
		read := make(map[string]bool, len(r.Playbooks))
		for _, pb := range r.Playbooks {
			read[pb.Path] = true
		}
		leftOut := make(map[string]bool, len(r.LeftOut))
		for _, p := range r.LeftOut {
			leftOut[p] = true
		}
		for _, pb := range prevPlaybooks {
			if leftOut[pb.Path] && !read[pb.Path] {
				playbooks = append(playbooks, pb)
			}
		}
	}
	return SourcesRecord{
		Commit: commit, SyncedAt: at, WorkerReads: true,
		Incomplete: r.Incomplete, LeftOut: r.LeftOut, Notes: r.Notes,
	}, playbooks, nil
}

// decodeReport checks and decodes a worker's report. Numbers stay
// json.Number, so a default stores exactly as the worker sent it.
func decodeReport(raw json.RawMessage) (varsource.Report, error) {
	if len(raw) > varsource.MaxReportBytes {
		return varsource.Report{}, fmt.Errorf("the report is %d bytes, more than %d", len(raw), varsource.MaxReportBytes)
	}
	// jsonb cannot hold U+0000; a report carrying one would fail the
	// finish on every retry.
	if hasNULEscape(raw) {
		return varsource.Report{}, errors.New("the report contains a NUL character")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var r *varsource.Report
	if err := dec.Decode(&r); err != nil {
		return varsource.Report{}, fmt.Errorf("decode the report: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return varsource.Report{}, errors.New("decode the report: data after the report")
	}
	if r == nil {
		return varsource.Report{}, errors.New("the report is null")
	}
	if len(r.Playbooks) > varsource.MaxPlaybooks {
		return varsource.Report{}, fmt.Errorf("the report has %d playbooks, more than %d", len(r.Playbooks), varsource.MaxPlaybooks)
	}
	seen := make(map[string]bool, len(r.Playbooks))
	for _, pb := range r.Playbooks {
		if pb.Path == "" {
			return varsource.Report{}, errors.New("a playbook has no path")
		}
		if seen[pb.Path] {
			return varsource.Report{}, fmt.Errorf("the playbook %q is reported twice", pb.Path)
		}
		seen[pb.Path] = true
	}
	return *r, nil
}

// hasNULEscape reports whether the JSON text raw carries a \u0000 escape:
// one whose backslash is not itself escaped. (A raw NUL byte is not valid
// JSON and fails the decode.)
func hasNULEscape(raw []byte) bool {
	const esc = `\u0000`
	for i := 0; ; {
		j := bytes.Index(raw[i:], []byte(esc))
		if j < 0 {
			return false
		}
		at := i + j
		backslashes := 0
		for k := at; k >= 0 && raw[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			return true
		}
		i = at + len(esc)
	}
}

// VarSources is the repository's record and every playbook row; found is
// false when no sync has recorded any.
func (s *Service) VarSources(ctx context.Context, orgID, repoID uuid.UUID) (SourcesRecord, []varsource.Playbook, bool, error) {
	rec, err := s.store.GetVarSources(ctx, orgID, repoID)
	if errors.Is(err, ErrNotFound) {
		return SourcesRecord{}, nil, false, nil
	}
	if err != nil {
		return SourcesRecord{}, nil, false, err
	}
	playbooks, err := s.store.ListPlaybookSources(ctx, orgID, repoID)
	if err != nil {
		return SourcesRecord{}, nil, false, err
	}
	return rec, playbooks, true, nil
}

// PlaybookVarSource is the repository's record and the row of one
// playbook path; found is false when there is no record or no row for the
// path.
func (s *Service) PlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (SourcesRecord, varsource.Playbook, bool, error) {
	rec, err := s.store.GetVarSources(ctx, orgID, repoID)
	if errors.Is(err, ErrNotFound) {
		return SourcesRecord{}, varsource.Playbook{}, false, nil
	}
	if err != nil {
		return SourcesRecord{}, varsource.Playbook{}, false, err
	}
	pb, err := s.store.GetPlaybookSource(ctx, orgID, repoID, path)
	if errors.Is(err, ErrNotFound) {
		return SourcesRecord{}, varsource.Playbook{}, false, nil
	}
	if err != nil {
		return SourcesRecord{}, varsource.Playbook{}, false, err
	}
	return rec, pb, true, nil
}
