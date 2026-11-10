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
	"log/slog"
	"strconv"
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

// MaxStoredPathBytes bounds a reported playbook path the manager stores:
// the path is part of an index key, which Postgres caps at about 2.7 KB.
// A worker reads no path past 200 characters (varsource), so only a
// report from elsewhere reaches it.
const MaxStoredPathBytes = 1024

// maxReportDepth bounds how deeply a report's JSON nests. A real report
// stays far below it; a deeper one could exhaust the database's parser
// stack when stored and fail the finish.
const maxReportDepth = 64

// RecordVarSources stores the variable sources report raw a successful
// sync of the repository sent, read at at. A nil raw is a sync by a worker
// that reads no sources: the previous record is kept and flagged.
//
// It runs inside the caller's transaction (the job finish's): the
// repository is locked first, then the previous record is read and merged
// with the report, so two overlapping finishes of one repository take
// turns. What the record becomes:
//   - a Failed report (nothing of the repository read) keeps the previous
//     commit and playbook rows and stores only its notes and at;
//   - otherwise the playbooks are replaced by the report's, except those
//     named in LeftOut, which keep their previous rows (a left-out name
//     without one gets none: it was never read);
//   - an empty commit (the worker could not read it) keeps the previous
//     commit, it is never a new revision;
//   - a playbook whose path is longer than MaxStoredPathBytes is dropped,
//     with a path_too_long note in the record and a log line.
//
// A report that is malformed or would not store is logged and ignored,
// the record untouched, and so is a repository deleted meanwhile: neither
// may fail the sync. Only a store failure is returned. stored reports
// whether the report's playbooks were stored: false for a nil raw, a
// Failed or ignored report and a gone repository, the cases with no new
// sources to apply.
func (s *Service) RecordVarSources(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, raw json.RawMessage) (bool, error) {
	if raw == nil {
		return false, s.store.MarkVarSourcesUnread(ctx, orgID, repoID, at)
	}
	r, dropped, err := decodeReport(raw)
	if err != nil {
		s.logger().WarnContext(ctx, "variable sources ignored", "org", orgID, "repo", repoID, "err", err)
		return false, nil
	}
	if len(dropped) > 0 {
		s.logger().WarnContext(ctx, "variable sources: playbook paths too long to store dropped",
			"org", orgID, "repo", repoID, "count", len(dropped), "limit", MaxStoredPathBytes)
		for _, p := range dropped {
			r.Notes = append(r.Notes, varsource.PathTooLong(p, MaxStoredPathBytes))
		}
	}
	if err := s.store.LockVarSources(ctx, orgID, repoID); err != nil {
		return false, s.skipGone(ctx, orgID, repoID, err)
	}
	rec, playbooks, err := s.nextSources(ctx, orgID, repoID, at, r)
	if err != nil {
		return false, err
	}
	if err := s.store.ReplaceVarSources(ctx, orgID, repoID, rec, playbooks); err != nil {
		return false, s.skipGone(ctx, orgID, repoID, err)
	}
	return !r.Failed, nil
}

// skipGone logs a repository gone meanwhile and drops that error; any
// other is returned.
func (s *Service) skipGone(ctx context.Context, orgID, repoID uuid.UUID, err error) error {
	if errors.Is(err, ErrNotFound) {
		s.logger().WarnContext(ctx, "variable sources skipped: repository no longer exists", "org", orgID, "repo", repoID)
		return nil
	}
	return fmt.Errorf("store variable sources: %w", err)
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

// decodeReport checks and decodes a worker's report, so that nothing in
// it can fail the store's write: dropped are the playbook paths too long
// to store (left out of the report returned).
func decodeReport(raw json.RawMessage) (r varsource.Report, dropped []string, err error) {
	if len(raw) > varsource.MaxReportBytes {
		return r, nil, fmt.Errorf("the report is %d bytes, more than %d", len(raw), varsource.MaxReportBytes)
	}
	depth, nul, unplain := scanJSON(raw)
	if depth > maxReportDepth {
		return r, nil, fmt.Errorf("the report nests %d levels deep, more than %d", depth, maxReportDepth)
	}
	// jsonb cannot hold U+0000; a report carrying one would fail the
	// finish on every retry. (Invalid UTF-8 the decode replaces.)
	if nul {
		return r, nil, errors.New("the report contains a NUL character")
	}
	// jsonb writes such a number out in full (1e300 as 301 digits), so a
	// stored result could grow far past the report cap; a current worker
	// never sends one (varsource.PlainFloat). The whole report is refused
	// (backlog 7.122): only a worker older than that check sends one, and
	// the previous record stands until it is upgraded.
	if unplain {
		return r, nil, errors.New("the report holds a number of 1e21 or more, or below 1e-6, which a current worker never sends")
	}
	var decoded *varsource.Report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return r, nil, fmt.Errorf("decode the report: %w", err)
	}
	if decoded == nil {
		return r, nil, errors.New("the report is null")
	}
	r = *decoded
	if len(r.Playbooks) > varsource.MaxPlaybooks {
		return r, nil, fmt.Errorf("the report has %d playbooks, more than %d", len(r.Playbooks), varsource.MaxPlaybooks)
	}
	kept := make([]varsource.Playbook, 0, len(r.Playbooks))
	seen := make(map[string]bool, len(r.Playbooks))
	for _, pb := range r.Playbooks {
		switch {
		case pb.Path == "":
			return r, nil, errors.New("a playbook has no path")
		case len(pb.Path) > MaxStoredPathBytes:
			dropped = append(dropped, pb.Path)
			continue
		case seen[pb.Path]:
			return r, nil, fmt.Errorf("the playbook %q is reported twice", pb.Path)
		}
		seen[pb.Path] = true
		kept = append(kept, pb)
	}
	r.Playbooks = kept
	return r, dropped, nil
}

// scanJSON reports how deeply the JSON text raw nests, whether it carries
// a \u0000 escape (one whose backslash is not itself escaped), and whether
// a number outside a string is unplain: out of float64's range, or one Go
// writes in exponent form (not varsource.PlainFloat). A raw NUL byte is
// not valid JSON and fails the decode, as does a malformed number.
func scanJSON(raw []byte) (maxDepth int, nul, unplain bool) {
	depth := 0
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case inString && c == '\\':
			i++
			if i < len(raw) && raw[i] == 'u' && bytes.HasPrefix(raw[i+1:], []byte("0000")) {
				nul = true
			}
		case inString && c == '"':
			inString = false
		case inString:
		case c == '"':
			inString = true
		case c == '{' || c == '[':
			depth++
			maxDepth = max(maxDepth, depth)
		case c == '}' || c == ']':
			depth--
		case c == '-' || isDigit(c):
			end := i + 1
			for end < len(raw) && isNumberByte(raw[end]) {
				end++
			}
			unplain = unplain || unplainNumber(raw[i:end])
			i = end - 1
		}
	}
	return maxDepth, nul, unplain
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isNumberByte(c byte) bool {
	return isDigit(c) || c == 'e' || c == 'E' || c == '.' || c == '+' || c == '-'
}

// unplainNumber reports a number token out of float64's range or not
// plain (varsource.PlainFloat). A malformed token is not reported here:
// the decode refuses it.
func unplainNumber(tok []byte) bool {
	x, err := strconv.ParseFloat(string(tok), 64)
	if errors.Is(err, strconv.ErrRange) {
		return true
	}
	return err == nil && !varsource.PlainFloat(x)
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
// playbook path, from one store read so the two belong together: rec is
// nil when no sync recorded any, pb nil when the record has no row for the
// path.
func (s *Service) PlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (rec *SourcesRecord, pb *varsource.Playbook, err error) {
	r, row, err := s.store.GetPlaybookVarSource(ctx, orgID, repoID, path)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &r, row, nil
}
