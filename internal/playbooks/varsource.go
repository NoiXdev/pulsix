package playbooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

// The kinds of VarSource.
const (
	VarSourceRepo   = "repo"
	VarSourceInline = "inline"
)

// VarSource is what a playbook's variable sources were last read as
// (backlog 7.75): a repo playbook's from its repository's last sync, an
// editor playbook's from its own text.
type VarSource struct {
	Kind string // VarSourceRepo or VarSourceInline
	// Rev names the read: a repo playbook's commit ("" without a record,
	// and "" for a playbook the last sync left out, whose fields are from
	// an earlier read), an editor playbook's "text:" and the first 12 hex
	// characters of the SHA-256 of its text.
	Rev string
	// SyncedAt is when the repository's record was written; nil for an
	// editor playbook and without a record.
	SyncedAt *time.Time
	// WorkerReads is false when the repository has no record or its last
	// sync came from a worker that reads no sources; always true inline.
	WorkerReads bool
	// Incomplete: a cap left playbooks of the repository unread.
	Incomplete bool
	// LeftOut: the last sync's caps left this playbook unread. Its fields,
	// if Found, are from an earlier read whose commit is not known, so Rev
	// is empty then.
	LeftOut bool
	// Found: a parse result exists for this playbook (which may itself be
	// Failed, i.e. not read).
	Found    bool
	RepoID   *uuid.UUID
	Path     string
	Playbook varsource.Playbook
}

// InlineApplier brings an editor playbook's new variable sources to the
// job templates that use it in automatic mode (plan decision 6: the inline
// analogue of "after each successful repo sync").
type InlineApplier interface {
	ApplyPlaybookSources(ctx context.Context, orgID, playbookID uuid.UUID) error
}

// inlineApplyTimeout bounds the apply after a save, as the apply after a
// repo sync is bounded.
const inlineApplyTimeout = 30 * time.Second

// SetInlineApplier sets what UpdateEditor calls after a save; nothing is
// applied without one.
func (s *Service) SetInlineApplier(a InlineApplier) { s.applier = a }

// SetLogger sets where the service reports what it does not let fail a
// save (an apply error, a parse result it could not store); slog.Default
// without.
func (s *Service) SetLogger(l *slog.Logger) { s.log = l }

func (s *Service) logger() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// VarSource is the playbook's variable sources as last read; ErrNotFound
// when the playbook does not exist in the organization.
func (s *Service) VarSource(ctx context.Context, orgID, playbookID uuid.UUID) (VarSource, error) {
	p, err := s.store.Get(ctx, orgID, playbookID)
	if err != nil {
		return VarSource{}, err
	}
	if p.Source != SourceRepo || p.RepoID == nil || p.Path == nil {
		return s.inlineVarSource(ctx, p), nil
	}
	src := VarSource{Kind: VarSourceRepo, RepoID: p.RepoID, Path: *p.Path}
	rec, pb, err := s.repos.PlaybookVarSource(ctx, orgID, *p.RepoID, *p.Path)
	if err != nil {
		return VarSource{}, fmt.Errorf("read the repository's variable sources: %w", err)
	}
	if rec != nil {
		at := rec.SyncedAt
		src.Rev, src.SyncedAt, src.WorkerReads, src.Incomplete = rec.Commit, &at, rec.WorkerReads, rec.Incomplete
		// A left-out playbook keeps its row from an earlier read, whose
		// commit the record no longer holds: its fields are never labelled
		// with the new commit.
		if slices.Contains(rec.LeftOut, *p.Path) {
			src.LeftOut, src.Rev = true, ""
		}
	}
	if pb != nil {
		src.Found, src.Playbook = true, *pb
	}
	return src, nil
}

// inlineVarSource is an editor playbook's stored parse result, or its text
// parsed now when none is stored (saved before backlog 7.75, or a result
// that could not be stored or no longer decodes). A result the cap of
// MaxInlineSourceBytes left out reads as incomplete and left out, with the
// note saying why; the text is not parsed again. Nothing is written.
func (s *Service) inlineVarSource(ctx context.Context, p Playbook) VarSource {
	text := ""
	if p.Inline != nil {
		text = *p.Inline
	}
	src := VarSource{Kind: VarSourceInline, Rev: inlineRev(text), WorkerReads: true, Found: true}
	if p.InlineSource != nil {
		pb, leftOut, err := decodeInlineSource(p.InlineSource)
		if err == nil {
			src.Playbook = pb
			if leftOut {
				src.Incomplete, src.LeftOut, src.Rev = true, true, ""
			}
			return src
		}
		s.logger().WarnContext(ctx, "stored inline variable sources do not decode; parsing the text", "org", p.OrgID, "playbook", p.ID, "err", err)
	}
	src.Playbook = varsource.ParseInline(text)
	return src
}

// inlineName is the file name an inline playbook's sources and notes carry
// (varsource.ParseInline's, and the name the report's left-out list holds).
const inlineName = "inline"

// decodeInlineSource reads what inlineSource stored: a report holding the
// playbook, or, when the cap left it out, no playbook (leftOut) and a note.
// A row saved before backlog 7.114 holds the bare playbook. Whatever the
// form, a row longer than MaxInlineSourceBytes reads as left out without
// being decoded.
func decodeInlineSource(raw json.RawMessage) (pb varsource.Playbook, leftOut bool, err error) {
	// A row over the cap is never decoded: rows saved before backlog 7.114
	// hold the bare playbook, which was uncapped.
	if len(raw) > MaxInlineSourceBytes {
		return leftOutPlaybook([]varsource.Note{{
			Level: varsource.LevelWarning, Code: "report_too_large",
			Message: fmt.Sprintf("the stored variable sources are larger than %d bytes and are left out; save the playbook again to read them", MaxInlineSourceBytes),
		}}), true, nil
	}
	var probe struct {
		Playbooks *json.RawMessage `json:"playbooks"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return pb, false, err
	}
	if probe.Playbooks == nil {
		return pb, false, json.Unmarshal(raw, &pb)
	}
	var rep varsource.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return pb, false, err
	}
	switch {
	case len(rep.Playbooks) == 1:
		return rep.Playbooks[0], false, nil
	case len(rep.Playbooks) == 0 && rep.Incomplete:
		return leftOutPlaybook(rep.Notes), true, nil
	}
	return pb, false, fmt.Errorf("the stored report holds %d playbooks", len(rep.Playbooks))
}

// leftOutPlaybook stands for an inline playbook whose result is not read:
// no fields, the notes saying why.
func leftOutPlaybook(notes []varsource.Note) varsource.Playbook {
	return varsource.Playbook{
		Path: inlineName, Failed: true, Fields: []schema.Field{}, SetInPlaybook: []string{},
		Roles: []varsource.RoleUse{}, Files: []varsource.FileUse{}, Notes: notes,
	}
}

// inlineRev is an inline text's revision: "text:" and the first 12 hex
// characters of its SHA-256.
func inlineRev(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "text:" + hex.EncodeToString(sum[:])[:12]
}

// MaxInlineSourceBytes caps the parse result stored with an editor playbook
// (backlog 7.114). Aliases let a text of 67 KB parse to about 6 MB, and
// every read of the playbook loads the result.
const MaxInlineSourceBytes = 1 << 20

// inlineSource parses an editor playbook's text for storing with it. The
// parse is bounded and never fails (a problem is a note); the result is
// stored as a one-playbook report cut by the repository sync's encoder to
// MaxInlineSourceBytes, so a result that does not fit is stored as left out
// and incomplete instead: a report without the playbook, so without a NUL
// character from it, and stored whatever the playbook held. A result that
// fits but that the database cannot hold (a NUL character, which its JSON
// type refuses) is not stored, and VarSource parses the text on read
// instead; that result is at most MaxInlineSourceBytes.
func (s *Service) inlineSource(ctx context.Context, orgID uuid.UUID, text string) json.RawMessage {
	raw, _, err := varsource.Encode(varsource.Report{
		Playbooks: []varsource.Playbook{varsource.ParseInline(text)}, Notes: []varsource.Note{},
	}, MaxInlineSourceBytes)
	if err == nil && hasNULEscape(raw) {
		err = errors.New("the result contains a NUL character")
	}
	if err != nil {
		s.logger().WarnContext(ctx, "inline variable sources not stored; parsed on read", "org", orgID, "err", err)
		return nil
	}
	return raw
}

// hasNULEscape reports whether the JSON text raw encodes U+0000: a
// \u0000 escape whose backslash is not itself escaped. (Outside strings
// JSON has no backslashes, so every one starts an escape.)
func hasNULEscape(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if bytes.HasPrefix(raw[i+1:], []byte("u0000")) {
			return true
		}
		i++ // the escaped character
	}
	return false
}

// applyInline hands a saved editor playbook to the applier, on a context
// detached from the request and bounded to inlineApplyTimeout; a failure
// is logged.
func (s *Service) applyInline(ctx context.Context, orgID, playbookID uuid.UUID) {
	if s.applier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inlineApplyTimeout)
	defer cancel()
	if err := s.applier.ApplyPlaybookSources(ctx, orgID, playbookID); err != nil {
		s.logger().ErrorContext(ctx, "apply inline playbook variable sources", "org", orgID, "playbook", playbookID, "err", err)
	}
}

// DiscoveredPlaybook is one playbook the repository's last sync recorded.
type DiscoveredPlaybook struct {
	Path     string
	Fields   int // top-level fields read
	Warnings int // notes of level warning
	// Failed: the playbook was not read; its counts mean nothing.
	Failed bool
	// LeftOut: the last sync's caps left it unread; the counts are from
	// the read before.
	LeftOut bool
	// PlaybookID is the playbook registered for the path, if any (the
	// oldest one).
	PlaybookID *uuid.UUID
}

// Discovery is a repository's last recorded read: Record nil and no
// playbooks when no sync recorded one.
type Discovery struct {
	Record    *repos.SourcesRecord
	Playbooks []DiscoveredPlaybook
}

// Discovered lists the playbooks the repository's last sync read, by path.
// A name the record lists as left out without a row of its own is not a
// confirmed playbook and is not listed (it stays in Record.LeftOut).
// ErrNotFound when the repository does not exist in the organization.
func (s *Service) Discovered(ctx context.Context, orgID, repoID uuid.UUID) (Discovery, error) {
	if err := s.repoExists(ctx, orgID, repoID); err != nil {
		return Discovery{}, err
	}
	rec, rows, found, err := s.repos.VarSources(ctx, orgID, repoID)
	if err != nil {
		return Discovery{}, fmt.Errorf("read the repository's variable sources: %w", err)
	}
	out := Discovery{Playbooks: []DiscoveredPlaybook{}}
	if !found {
		return out, nil
	}
	registered, err := s.store.ListByRepo(ctx, orgID, repoID)
	if err != nil {
		return Discovery{}, err
	}
	ids := make(map[string]uuid.UUID, len(registered))
	for _, p := range registered {
		if p.Path == nil {
			continue
		}
		if _, seen := ids[*p.Path]; !seen {
			ids[*p.Path] = p.ID
		}
	}
	leftOut := make(map[string]bool, len(rec.LeftOut))
	for _, name := range rec.LeftOut {
		leftOut[name] = true
	}
	out.Record = &rec
	for _, pb := range rows {
		d := DiscoveredPlaybook{Path: pb.Path, Fields: len(pb.Fields), Failed: pb.Failed, LeftOut: leftOut[pb.Path]}
		for _, n := range pb.Notes {
			if n.Level == varsource.LevelWarning {
				d.Warnings++
			}
		}
		if id, ok := ids[pb.Path]; ok {
			d.PlaybookID = &id
		}
		out.Playbooks = append(out.Playbooks, d)
	}
	return out, nil
}

// EnsureRepo is the playbook registered for the repository path, made when
// there is none (created true). The lookup and the insert run under one
// lock on the path, so two picks of one path make one row. ErrInvalidInput
// for a path no playbook may have, ErrNotFound for an unknown repository.
func (s *Service) EnsureRepo(ctx context.Context, orgID, repoID uuid.UUID, path string) (Playbook, bool, error) {
	if err := validRepoPath(path); err != nil {
		return Playbook{}, false, err
	}
	var (
		p       Playbook
		created bool
	)
	err := s.store.WithRepoPathLock(ctx, orgID, repoID, path, func(ctx context.Context) error {
		found, err := s.store.FindRepo(ctx, orgID, repoID, path)
		if err == nil {
			p = found
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("find repository playbook: %w", err)
		}
		if p, err = s.CreateRepo(ctx, orgID, repoID, path); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return Playbook{}, false, err
	}
	return p, created, nil
}

// repoExists maps an unknown repository to ErrNotFound.
func (s *Service) repoExists(ctx context.Context, orgID, repoID uuid.UUID) error {
	if _, err := s.repos.Get(ctx, orgID, repoID); err != nil {
		if errors.Is(err, repos.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("look up repository: %w", err)
	}
	return nil
}
