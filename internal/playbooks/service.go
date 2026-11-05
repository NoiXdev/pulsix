// Package playbooks stores the playbooks jobs run against: a path inside a
// registered git repository, or an inline YAML document authored in the
// editor (spec §7, backlog 2.9). The playbooks table enforces the two
// sources with CHECK constraints this package mirrors in Go; inline text is
// validated here, and the worker's own pre-run syntax check remains the
// real gate at run time.
package playbooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/varsource"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	// ErrInUse means a job template still references the playbook.
	ErrInUse = errors.New("playbook is in use")
)

// Source tells a playbook's origin. The values are the stored ones — the
// playbooks table's source CHECK allows exactly these two strings.
const (
	SourceRepo   = "repo"
	SourceEditor = "editor"
)

// RepoPathPattern is a repo playbook's path within its repository: a
// relative path of letters, digits, dots, slashes and hyphens that ends in
// .yml or .yaml and never climbs directories (".."). 201 characters plus
// the extension keep the total within the schema's 200-character intent.
// It is the pattern the repo sync discovers playbooks by, so every
// discovered path can be picked.
var RepoPathPattern = varsource.PlaybookPathPattern

// Playbook is one stored playbook. Exactly one of RepoID/Path (repo source)
// or Inline (editor source) is set — the database CHECK constraints enforce
// the pairing, and Create* enforce it before anything reaches the store.
type Playbook struct {
	ID     uuid.UUID
	OrgID  uuid.UUID
	RepoID *uuid.UUID
	Path   *string
	Inline *string
	Source string
	// InlineSource is an editor playbook's stored variable sources, the
	// encoded varsource.ParseInline result of Inline (backlog 7.75). It is
	// nil for a repo playbook, for an editor playbook saved before it
	// existed, and when the result could not be stored; VarSource then
	// parses the text.
	InlineSource json.RawMessage
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateInput is a new editor playbook. Name is not a stored column — the
// playbook's identity is its content — but is accepted so callers can pass
// the form's name field along for audit summaries.
type CreateInput struct {
	Name   string
	Inline string
}

// RepoSource is the slice of repos.Service a repo playbook needs: the
// referenced repository must exist in the organization, and its last
// sync's variable sources are read for the discovered playbooks and a
// repo playbook's VarSource.
type RepoSource interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (repos.Repo, error)
	VarSources(ctx context.Context, orgID, repoID uuid.UUID) (repos.SourcesRecord, []varsource.Playbook, bool, error)
	PlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (*repos.SourcesRecord, *varsource.Playbook, error)
}

// TemplateResolver is the slice of shared.Service a new repo playbook
// needs (spec v0.2f, Task 6's own review finding): registering
// (orgID, repoID, path) here is the ORGANIZATION-side event that makes a
// shared job template's projection resolvable in the first place, and
// nothing fired on it before this port existed. shared.Service.
// CreateTemplate/UpdateTemplate already resolve playbook_id per
// organization, but only on a DEFINITION-side event — a provider defining
// a shared template before any customer has synced its matching playbook
// (the normal order: configure centrally, then customers catch up) left
// that projection's playbook_id NULL until the definition's next edit,
// which may never come. CreateRepo below calls this once the playbook
// itself is safely stored, so a template an organization could not run a
// minute ago becomes runnable the moment it registers the matching path,
// with no action on the provider's part.
type TemplateResolver interface {
	ResolveTemplatePlaybooks(ctx context.Context, orgID, repoID, playbookID uuid.UUID, path string) error
}

// Store persists playbooks. Every lookup that finds nothing returns
// ErrNotFound; deleting a playbook a job template still references is
// ErrInUse.
type Store interface {
	Create(ctx context.Context, p Playbook) (Playbook, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Playbook, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Playbook, error)
	// UpdateInline replaces the inline text of an editor playbook and its
	// stored variable sources (nil stores none). A repo playbook's stored
	// text lives in its git repository and has no inline column to update.
	UpdateInline(ctx context.Context, orgID, id uuid.UUID, inline string, source json.RawMessage) (Playbook, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	// FindRepo is the oldest playbook registered for the repository path.
	FindRepo(ctx context.Context, orgID, repoID uuid.UUID, path string) (Playbook, error)
	// ListByRepo is every playbook registered for the repository, oldest
	// first; only ID, OrgID, RepoID, Path and Source are set.
	ListByRepo(ctx context.Context, orgID, repoID uuid.UUID) ([]Playbook, error)
	// WithRepoPathLock runs fn in one write transaction that holds a lock
	// on the repository path; every store call made with the context fn
	// receives joins it.
	WithRepoPathLock(ctx context.Context, orgID, repoID uuid.UUID, path string, fn func(context.Context) error) error
}

// Service implements the playbook registry.
type Service struct {
	store    Store
	repos    RepoSource
	resolver TemplateResolver
	applier  InlineApplier
	log      *slog.Logger
}

// NewService wires the store, the repo source and the template resolver
// (TemplateResolver's own doc comment: the organization-side half of
// shared job template resolution).
func NewService(store Store, repoSource RepoSource, resolver TemplateResolver) *Service {
	return &Service{store: store, repos: repoSource, resolver: resolver}
}

// CreateEditor validates the inline document and stores it as an editor
// playbook. Name is not persisted (editor playbooks carry no name column);
// list summaries are computed from the YAML itself.
func (s *Service) CreateEditor(ctx context.Context, orgID uuid.UUID, in CreateInput) (Playbook, error) {
	if err := ValidateYAML(in.Inline); err != nil {
		return Playbook{}, err
	}
	return s.store.Create(ctx, Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
		Source: SourceEditor, Inline: &in.Inline,
		InlineSource: s.inlineSource(ctx, orgID, in.Inline),
	})
}

// CreateRepo stores a playbook reference to a path inside a registered
// repository. The path is validated here and the repository must exist.
//
// Once the row is stored, this is also the organization-side resolution
// event TemplateResolver's own doc comment describes: whether repoID
// projects a shared repository definition or not, and whether any shared
// template definition names this exact path or not, is for
// shared.Service.ResolveTemplatePlaybooks to decide, not this package —
// playbooks must not need to know what a "shared template" is to report
// that a path now exists. A resolution failure is reported rather than
// swallowed: a caller that successfully registered a playbook but silently
// failed to light up a template waiting on it would be a harder bug to
// find than one that surfaces here, at the one call site that can.
func (s *Service) CreateRepo(ctx context.Context, orgID, repoID uuid.UUID, path string) (Playbook, error) {
	if err := validRepoPath(path); err != nil {
		return Playbook{}, err
	}
	if err := s.repoExists(ctx, orgID, repoID); err != nil {
		return Playbook{}, err
	}
	p, err := s.store.Create(ctx, Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
		Source: SourceRepo, RepoID: &repoID, Path: &path,
	})
	if err != nil {
		return Playbook{}, err
	}
	if err := s.resolver.ResolveTemplatePlaybooks(ctx, orgID, repoID, p.ID, path); err != nil {
		return Playbook{}, fmt.Errorf("resolve shared template playbooks: %w", err)
	}
	return p, nil
}

// validRepoPath refuses a path no repo playbook may have: outside
// RepoPathPattern, with "..", or not in the one canonical spelling a sync
// discovers it under (no leading "/" or "./", no "//", no "." segment), so
// one file is never registered under two paths.
func validRepoPath(p string) error {
	if strings.Contains(p, "..") || !RepoPathPattern.MatchString(p) ||
		strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fmt.Errorf("%w: invalid path: a relative path ending in .yml or .yaml (letters, digits, dots, slashes and hyphens), no \"..\", \".\" or empty segments, no leading slash", ErrInvalidInput)
	}
	return nil
}

// Get returns one playbook, ErrNotFound when it does not exist in the org.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Playbook, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns every playbook of the organization.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Playbook, error) {
	return s.store.List(ctx, orgID)
}

// UpdateEditor replaces an editor playbook's inline document and its
// stored variable sources. A repo playbook is edited in its repository,
// not here — ErrInvalidInput. Once saved, the InlineApplier (if set)
// brings the new sources to the templates that use the playbook; its
// failure is logged and never fails the save.
func (s *Service) UpdateEditor(ctx context.Context, orgID, id uuid.UUID, in CreateInput) (Playbook, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Playbook{}, err
	}
	if cur.Source != SourceEditor {
		return Playbook{}, fmt.Errorf("%w: a repo playbook is edited in its repository", ErrInvalidInput)
	}
	if err := ValidateYAML(in.Inline); err != nil {
		return Playbook{}, err
	}
	p, err := s.store.UpdateInline(ctx, orgID, id, in.Inline, s.inlineSource(ctx, orgID, in.Inline))
	if err != nil {
		return Playbook{}, err
	}
	s.applyInline(ctx, orgID, id)
	return p, nil
}

// Delete removes a playbook no job template references; the foreign key is
// the guard (no cascade), so a referenced playbook is ErrInUse.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// Summary is the short list-view label of an editor playbook: its first
// play's name key when the document names one, else the first 80 characters
// of the inline text. Repo playbooks summarize as nothing — their path is
// the label — and a stored document that no longer parses falls back to the
// raw text, never an error: a summary must not hide the list.
//
// The summary is computed, never stored: editor playbooks carry no name
// column.
func (p Playbook) Summary() string {
	if p.Inline == nil {
		return ""
	}
	if name, ok := firstPlayName(*p.Inline); ok {
		return name
	}
	inline := []rune(*p.Inline)
	if len(inline) > 80 {
		inline = inline[:80]
	}
	return string(inline)
}
