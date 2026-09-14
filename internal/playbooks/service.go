// Package playbooks stores the playbooks jobs run against: a path inside a
// registered git repository, or an inline YAML document authored in the
// editor (spec §7, backlog 2.9). The playbooks table enforces the two
// sources with CHECK constraints this package mirrors in Go; inline text is
// validated here, and the worker's own pre-run syntax check remains the
// real gate at run time.
package playbooks

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/repos"
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
var RepoPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}\.ya?ml$`)

// Playbook is one stored playbook. Exactly one of RepoID/Path (repo source)
// or Inline (editor source) is set — the database CHECK constraints enforce
// the pairing, and Create* enforce it before anything reaches the store.
type Playbook struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	RepoID    *uuid.UUID
	Path      *string
	Inline    *string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is a new editor playbook. Name is not a stored column — the
// playbook's identity is its content — but is accepted so callers can pass
// the form's name field along for audit summaries.
type CreateInput struct {
	Name   string
	Inline string
}

// RepoSource is the slice of repos.Service a repo playbook needs: the
// referenced repository must exist in the organization.
type RepoSource interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (repos.Repo, error)
}

// Store persists playbooks. Every lookup that finds nothing returns
// ErrNotFound; deleting a playbook a job template still references is
// ErrInUse.
type Store interface {
	Create(ctx context.Context, p Playbook) (Playbook, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Playbook, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Playbook, error)
	// UpdateInline replaces the inline text of an editor playbook. A repo
	// playbook's stored text lives in its git repository and has no inline
	// column to update.
	UpdateInline(ctx context.Context, orgID, id uuid.UUID, inline string) (Playbook, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// Service implements the playbook registry.
type Service struct {
	store Store
	repos RepoSource
}

// NewService wires the store and the repo source.
func NewService(store Store, repoSource RepoSource) *Service {
	return &Service{store: store, repos: repoSource}
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
	})
}

// CreateRepo stores a playbook reference to a path inside a registered
// repository. The path is validated here and the repository must exist.
func (s *Service) CreateRepo(ctx context.Context, orgID, repoID uuid.UUID, path string) (Playbook, error) {
	if strings.Contains(path, "..") || !RepoPathPattern.MatchString(path) {
		return Playbook{}, fmt.Errorf("%w: invalid path: a relative path ending in .yml or .yaml (letters, digits, dots, slashes and hyphens), no \"..\"", ErrInvalidInput)
	}
	if _, err := s.repos.Get(ctx, orgID, repoID); err != nil {
		if errors.Is(err, repos.ErrNotFound) {
			return Playbook{}, ErrNotFound
		}
		return Playbook{}, fmt.Errorf("look up repository: %w", err)
	}
	return s.store.Create(ctx, Playbook{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
		Source: SourceRepo, RepoID: &repoID, Path: &path,
	})
}

// Get returns one playbook, ErrNotFound when it does not exist in the org.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Playbook, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns every playbook of the organization.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Playbook, error) {
	return s.store.List(ctx, orgID)
}

// UpdateEditor replaces an editor playbook's inline document. A repo
// playbook is edited in its repository, not here — ErrInvalidInput.
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
	return s.store.UpdateInline(ctx, orgID, id, in.Inline)
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
