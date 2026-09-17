// Package repos is the git repository registry (spec §7, backlog 2.8): the
// Ansible playbook sources an organization registers, optionally bound to a
// git_key/git_token credential, with the sync action that enqueues a
// sync_repo job through the job queue. URLs are validated by pattern
// against https://, ssh:// and SCP-like git forms — never fetched.
package repos

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/jobs"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("name already in use")
	// ErrInUse means a playbook still references the repository.
	ErrInUse = errors.New("repository is in use")
)

// DefaultBranch is what an empty branch field becomes: the repo's checkout
// still has to name a branch, and "main" is today's overwhelmingly common
// default.
const DefaultBranch = "main"

// MaxURLLength bounds a repository URL; git URLs are short by construction
// and 500 characters leave generous room for path-style remotes.
const MaxURLLength = 500

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	urlPattern     = regexp.MustCompile(`^(https://[^/ ]+|ssh://[^/ ]+|[^@/ ]+@[^:/ ]+:)`)
	branchPattern  = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
	subpathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,200}$`)
)

// Repo is one registered git repository.
type Repo struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	Name         string
	URL          string
	Branch       string
	Subpath      string
	CredentialID *uuid.UUID
	// LastSync is when the last sync_repo job for this repo reached a
	// terminal state, and LastSyncError its failure reason ("" on success).
	LastSync      *time.Time
	LastSyncError string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CreateInput is a new repository. An empty Branch becomes DefaultBranch.
type CreateInput struct {
	Name         string
	URL          string
	Branch       string
	Subpath      string
	CredentialID *uuid.UUID
}

// UpdateInput replaces every editable field, name included; a name clash
// with another repository is ErrNameTaken.
type UpdateInput struct {
	Name         string
	URL          string
	Branch       string
	Subpath      string
	CredentialID *uuid.UUID
}

// CredentialSource is the slice of credentials.Service a repo's credential
// check needs: the referenced credential must exist and be git-keyed.
type CredentialSource interface {
	Get(ctx context.Context, orgID, id uuid.UUID) (credentials.Credential, error)
}

// JobEnqueuer is the slice of jobs.Service the sync action needs. Declared
// here so this package stays decoupled from the queue's construction; the
// concrete jobs.Service implements it directly.
type JobEnqueuer interface {
	EnqueueSync(ctx context.Context, orgID, repoID uuid.UUID, actor *uuid.UUID) (jobs.Job, error)
}

// Store persists repositories. Lookups that find nothing return
// ErrNotFound, a name clash ErrNameTaken, and deleting a referenced repo
// ErrInUse.
type Store interface {
	Create(ctx context.Context, r Repo) (Repo, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (Repo, error)
	List(ctx context.Context, orgID uuid.UUID) ([]Repo, error)
	Update(ctx context.Context, r Repo) (Repo, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// Service implements the repository registry.
type Service struct {
	store Store
	creds CredentialSource
	jobs  JobEnqueuer
}

// NewService wires the store, the credential source and the sync enqueuer.
func NewService(store Store, creds CredentialSource, jobs JobEnqueuer) *Service {
	return &Service{store: store, creds: creds, jobs: jobs}
}

// Create validates and registers a repository.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Repo, error) {
	if err := s.validate(ctx, orgID, in.CredentialID, in.Name, in.URL, in.Branch, in.Subpath); err != nil {
		return Repo{}, err
	}
	return s.store.Create(ctx, Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
		Name: in.Name, URL: in.URL, Branch: defaultBranch(in.Branch), Subpath: in.Subpath,
		CredentialID: in.CredentialID,
	})
}

// Get returns one repository, ErrNotFound when it does not exist in the org.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Repo, error) {
	return s.store.Get(ctx, orgID, id)
}

// List returns every repository of the organization, ordered by name.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Repo, error) {
	return s.store.List(ctx, orgID)
}

// Update replaces every editable field, name included (a clash with another
// repository's name is ErrNameTaken).
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateInput) (Repo, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Repo{}, err
	}
	if err := s.validate(ctx, orgID, in.CredentialID, in.Name, in.URL, in.Branch, in.Subpath); err != nil {
		return Repo{}, err
	}
	next := cur
	next.Name, next.URL, next.Branch, next.Subpath, next.CredentialID =
		in.Name, in.URL, defaultBranch(in.Branch), in.Subpath, in.CredentialID
	return s.store.Update(ctx, next)
}

// Delete removes a repository that no playbook references; the foreign key
// is the guard (no cascade), so a referenced repository is ErrInUse.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// Sync enqueues one sync_repo job for the repository (jobs.Service's
// EnqueueSync, through the local port). The repo must exist; the job is
// enqueued into the organization's default zone, which the jobs service
// resolves.
func (s *Service) Sync(ctx context.Context, orgID, id uuid.UUID, actor *uuid.UUID) (jobs.Job, error) {
	if _, err := s.store.Get(ctx, orgID, id); err != nil {
		return jobs.Job{}, err
	}
	job, err := s.jobs.EnqueueSync(ctx, orgID, id, actor)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("enqueue sync_repo: %w", err)
	}
	return job, nil
}

// validate checks every editable field, resolving the credential (when
// referenced) first so a wrong-kind credential fails before anything is
// stored.
func (s *Service) validate(ctx context.Context, orgID uuid.UUID, credentialID *uuid.UUID, name, url, branch, subpath string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: invalid name: must be lowercase letters, digits, dots, underscores or hyphens (1–63 characters), starting with a letter or digit", ErrInvalidInput)
	}
	if err := validateURL(url); err != nil {
		return err
	}
	if branch != "" && (strings.Contains(branch, "..") || strings.HasPrefix(branch, "-") || !branchPattern.MatchString(branch)) {
		return fmt.Errorf("%w: invalid branch: may contain letters, digits, dots, slashes and hyphens (1–100 characters), no \"..\", not starting with \"-\"", ErrInvalidInput)
	}
	if strings.Contains(subpath, "..") || !subpathPattern.MatchString(subpath) {
		return fmt.Errorf("%w: invalid subpath: may be empty or contain letters, digits, dots, slashes and hyphens (up to 200 characters), no \"..\"", ErrInvalidInput)
	}
	if credentialID == nil {
		return nil
	}
	cred, err := s.creds.Get(ctx, orgID, *credentialID)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return fmt.Errorf("%w: credential not found", ErrInvalidInput)
		}
		return fmt.Errorf("look up credential: %w", err)
	}
	if cred.Kind != credentials.KindGitKey && cred.Kind != credentials.KindGitToken {
		return fmt.Errorf("%w: the referenced credential must be a git_key or git_token credential", ErrInvalidInput)
	}
	return nil
}

func validateURL(raw string) error {
	switch {
	case raw == "":
		return fmt.Errorf("%w: invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL", ErrInvalidInput)
	case strings.HasPrefix(raw, "-"):
		// git would parse it as an option (-oProxyCommand=…).
		return fmt.Errorf("%w: invalid url: must not start with \"-\"", ErrInvalidInput)
	case strings.ContainsAny(raw, " \t\n\r"):
		return fmt.Errorf("%w: invalid url: must not contain whitespace", ErrInvalidInput)
	case len(raw) > MaxURLLength:
		return fmt.Errorf("%w: invalid url: exceeds %d characters", ErrInvalidInput, MaxURLLength)
	case !urlPattern.MatchString(raw):
		return fmt.Errorf("%w: invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL", ErrInvalidInput)
	}
	return nil
}

// defaultBranch maps the schema default onto Go: an empty branch input is
// stored as DefaultBranch.
func defaultBranch(branch string) string {
	if branch == "" {
		return DefaultBranch
	}
	return branch
}
