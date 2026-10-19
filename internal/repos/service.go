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

// Pin states (backlog 2.41), the repo mirror of the hosts/bastion shapes:
// a repository's pin is written by the sync sink from the key the worker
// observed on the first successful sync and always carries the full public
// key, so the state is full while pinned; unpinned is not a pin state (the
// DTO leaves the field out, like the hosts read does).
const PinStateFull = "full"

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
	ID      uuid.UUID
	OrgID   uuid.UUID
	Name    string
	URL     string
	Branch  string
	Subpath string
	// CredentialID is nil when the checkout fetches without a credential.
	CredentialID *uuid.UUID
	// VaultCredentialID names the vault_password credential Ansible needs
	// to read this repository's encrypted files, nil when it has none.
	// Deliberately separate from CredentialID beside it: one secret gets
	// AT the repository, the other at its CONTENTS, and a deploy key that
	// could also decrypt group_vars would be one secret doing two jobs.
	//
	// One repository, one vault password. Ansible supports several vault
	// ids for several environments; that would make this a list, and the
	// limit is recorded here, in migration 00055 and in the proto.
	//
	// v0.2e lifts that limit: VaultIdentities below is the labelled set a
	// repository actually carries. This column is kept (spec decision 5,
	// additive migrations only) and nothing reads it after that
	// sub-project — VaultIdentities is what Create/Update now write and
	// Get/List attach.
	VaultCredentialID *uuid.UUID
	// VaultIdentities is this repository's own labelled vault password set
	// (migration 00059, spec decision 1): the base set a run's job template
	// may add to but never sees reduced. Read separately from the row by
	// Get/List (ListRepoVaultIdentities) and written separately by
	// Create/Update (ReplaceRepoVaultIdentities) — it does not live in this
	// table. FromTemplate is always false on every entry here; the merge
	// with a template's own set happens at run time, in
	// ResolveVaultIdentities, never on this struct.
	VaultIdentities []VaultIdentity
	// SSHHostKey is the git server's pinned host-key fingerprint
	// (SHA256:…) and SSHHostPublicKey the full key in authorized-key form —
	// written by the sync sink from what the FIRST (unpinned) successful
	// sync observed (TOFU per repository, backlog 2.41), cleared by the
	// unpin action. Both empty while unpinned.
	SSHHostKey       string
	SSHHostPublicKey string
	// LastSync is when the last sync_repo job for this repo reached a
	// terminal state, and LastSyncError its failure reason ("" on success).
	LastSync      *time.Time
	LastSyncError string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PinState is the read APIs' pin state: full while the repository carries a
// pin (a repo pin always carries the full key), "" while unpinned — trust
// on first use is not a pin state.
func (r Repo) PinState() string {
	if r.SSHHostKey != "" {
		return PinStateFull
	}
	return ""
}

// CreateInput is a new repository. An empty Branch becomes DefaultBranch.
// VaultIdentities rides alongside VaultCredentialID, not in place of it: the
// old single-password slot stays live (spec decision 5), the new labelled
// set is this repository's actual passwords from v0.2e on.
type CreateInput struct {
	Name              string
	URL               string
	Branch            string
	Subpath           string
	CredentialID      *uuid.UUID
	VaultCredentialID *uuid.UUID
	VaultIdentities   []VaultIdentity
}

// UpdateInput replaces every editable field, name included; a name clash
// with another repository is ErrNameTaken. VaultIdentities is a full
// replacement too (PUT semantics, the same as every other field here): a
// label present before and absent from this call is dropped.
type UpdateInput struct {
	Name              string
	URL               string
	Branch            string
	Subpath           string
	CredentialID      *uuid.UUID
	VaultCredentialID *uuid.UUID
	VaultIdentities   []VaultIdentity
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
	// ClearHostKey removes the repository's git server key pin (backlog
	// 2.41's unpin); ErrNotFound when the repository does not exist in the
	// organization.
	ClearHostKey(ctx context.Context, orgID, id uuid.UUID) error
	// ReplaceRepoVaultIdentities replaces a repository's labelled vault
	// password set wholesale (spec decision 1); an empty or nil slice is
	// the legal "no vault passwords" result.
	ReplaceRepoVaultIdentities(ctx context.Context, orgID, repoID uuid.UUID, identities []VaultIdentity) error
	// ListRepoVaultIdentities returns a repository's labelled vault
	// password set, in label order.
	ListRepoVaultIdentities(ctx context.Context, orgID, repoID uuid.UUID) ([]VaultIdentity, error)
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

// Create validates and registers a repository, then stores its labelled
// vault identity set (ReplaceRepoVaultIdentities is always called, even with
// an empty slice, so Create and Update share one "this is now the full set"
// rule rather than Create having a silent special case for "none given").
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, in CreateInput) (Repo, error) {
	if err := s.validate(ctx, orgID, in.CredentialID, in.VaultCredentialID, in.Name, in.URL, in.Branch, in.Subpath, in.VaultIdentities); err != nil {
		return Repo{}, err
	}
	created, err := s.store.Create(ctx, Repo{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
		Name: in.Name, URL: in.URL, Branch: defaultBranch(in.Branch), Subpath: in.Subpath,
		CredentialID:      in.CredentialID,
		VaultCredentialID: in.VaultCredentialID,
	})
	if err != nil {
		return Repo{}, err
	}
	identities := legacyVaultIdentities(in.VaultCredentialID, in.VaultIdentities)
	if err := s.store.ReplaceRepoVaultIdentities(ctx, orgID, created.ID, identities); err != nil {
		return Repo{}, fmt.Errorf("store repo vault identities: %w", err)
	}
	created.VaultIdentities = identities
	return created, nil
}

// legacyVaultIdentities is the write-time bridge for VaultCredentialID, the
// single-password slot the claim path (internal/jobs) stopped reading once
// it moved to this link table (ListRepoVaultIdentities) — a repository
// configured solely through the old field would otherwise go silently
// passwordless on every run. The two inputs are never merged;
// VaultIdentities, whenever it is non-empty, is the whole truth:
//
//   - identities given: returned as-is; VaultCredentialID is still stored
//     on the row (an additive column, spec decision 5) but contributes
//     nothing to what a run receives.
//   - identities empty, VaultCredentialID set: synthesizes the one-entry
//     "default" set — the exact label migration 00059's backfill used, so
//     a repository configured this way and a migrated one are
//     indistinguishable to the claim path.
//   - both empty: no vault identities.
//
// This is temporary scaffolding, not a second permanent source of truth.
// The condition for retiring it — a VaultIdentities editor in the product's
// UI, so freezing this write path (refusing VaultCredentialID once anything
// is set) would not break the only working way to configure a repository's
// vault password — is now met: RepoDialog.tsx's VaultIdentitiesField edits
// the labelled set directly. Backlog 2.92 tracks removing this bridge (and
// 2.93 dropping the now-write-only VaultCredentialID column once a release
// has shipped without it).
func legacyVaultIdentities(vaultCredentialID *uuid.UUID, identities []VaultIdentity) []VaultIdentity {
	if len(identities) > 0 {
		return identities
	}
	if vaultCredentialID != nil {
		return []VaultIdentity{{CredentialID: *vaultCredentialID, Label: "default"}}
	}
	return identities
}

// Get returns one repository, with its vault identity set attached,
// ErrNotFound when it does not exist in the org.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Repo, error) {
	r, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Repo{}, err
	}
	identities, err := s.store.ListRepoVaultIdentities(ctx, orgID, id)
	if err != nil {
		return Repo{}, fmt.Errorf("list repo vault identities: %w", err)
	}
	r.VaultIdentities = identities
	return r, nil
}

// List returns every repository of the organization, ordered by name, each
// with its vault identity set attached.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Repo, error) {
	list, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		identities, err := s.store.ListRepoVaultIdentities(ctx, orgID, list[i].ID)
		if err != nil {
			return nil, fmt.Errorf("list repo vault identities: %w", err)
		}
		list[i].VaultIdentities = identities
	}
	return list, nil
}

// Update replaces every editable field, name included (a clash with another
// repository's name is ErrNameTaken). The git server key pin is not an
// editable field: it survives the edit unless the URL changes — the pin
// pins the server the URL names, so a moved checkout is unpinned and the
// next sync re-pins whatever the new server presents (backlog 2.41).
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateInput) (Repo, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Repo{}, err
	}
	if err := s.validate(ctx, orgID, in.CredentialID, in.VaultCredentialID, in.Name, in.URL, in.Branch, in.Subpath, in.VaultIdentities); err != nil {
		return Repo{}, err
	}
	next := cur
	next.Name, next.URL, next.Branch, next.Subpath, next.CredentialID, next.VaultCredentialID =
		in.Name, in.URL, defaultBranch(in.Branch), in.Subpath, in.CredentialID, in.VaultCredentialID
	if next.URL != cur.URL {
		next.SSHHostKey, next.SSHHostPublicKey = "", ""
	}
	updated, err := s.store.Update(ctx, next)
	if err != nil {
		return Repo{}, err
	}
	identities := legacyVaultIdentities(in.VaultCredentialID, in.VaultIdentities)
	if err := s.store.ReplaceRepoVaultIdentities(ctx, orgID, id, identities); err != nil {
		return Repo{}, fmt.Errorf("store repo vault identities: %w", err)
	}
	updated.VaultIdentities = identities
	return updated, nil
}

// Delete removes a repository that no playbook references; the foreign key
// is the guard (no cascade), so a referenced repository is ErrInUse.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.store.Delete(ctx, orgID, id)
}

// ClearHostKey removes the repository's git server key pin (backlog 2.41's
// unpin): the next successful sync pins whatever the server then presents
// (TOFU). ErrNotFound when the repository does not exist in the
// organization — a foreign repository is invisible, never cleared.
func (s *Service) ClearHostKey(ctx context.Context, orgID, id uuid.UUID) error {
	if _, err := s.store.Get(ctx, orgID, id); err != nil {
		return err
	}
	return s.store.ClearHostKey(ctx, orgID, id)
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
//
// The git credential check only runs when credentialID is set — a public
// repository legitimately has none. It used to return early in that case,
// before the vault checks below existed; that early return also skipped
// vaultCredentialID and vaultIdentities, which made "a public repository
// with encrypted vars" (no git credential, but a vault password) silently
// skip vault validation entirely. Fixed here: the git credential's own
// absence short-circuits only its own check now, never the two below it.
func (s *Service) validate(ctx context.Context, orgID uuid.UUID, credentialID, vaultCredentialID *uuid.UUID, name, url, branch, subpath string, vaultIdentities []VaultIdentity) error {
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
	if err := s.validateGitCredential(ctx, orgID, credentialID); err != nil {
		return err
	}
	if err := s.validateVaultCredential(ctx, orgID, vaultCredentialID); err != nil {
		return err
	}
	return s.validateVaultIdentities(ctx, orgID, vaultIdentities)
}

// validateGitCredential refuses anything but a git_key or git_token
// credential in the repository's checkout slot; nil (no credential: a
// public repository) is fine.
func (s *Service) validateGitCredential(ctx context.Context, orgID uuid.UUID, credentialID *uuid.UUID) error {
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

// validateVaultIdentities checks a repository's labelled vault password set:
// every label must pass InvalidVaultLabel (spec decision 4, amended — the
// same rule internal/worker/runner enforces again as its own last gate) and
// must be unique within the set — the table's own PRIMARY KEY (repo_id,
// label) would otherwise turn a duplicate into a 500 from the store instead
// of a 400 naming the mistake. Each credential must exist and be a
// vault_password credential, the same rule validateVaultCredential applies
// to the single VaultCredentialID slot beside this set.
func (s *Service) validateVaultIdentities(ctx context.Context, orgID uuid.UUID, identities []VaultIdentity) error {
	seen := make(map[string]bool, len(identities))
	for _, identity := range identities {
		if reason := InvalidVaultLabel(identity.Label); reason != "" {
			return fmt.Errorf("%w: invalid vault identity label %q: %s", ErrInvalidInput, identity.Label, reason)
		}
		if seen[identity.Label] {
			return fmt.Errorf("%w: duplicate vault identity label %q", ErrInvalidInput, identity.Label)
		}
		seen[identity.Label] = true
		credentialID := identity.CredentialID
		if err := s.validateVaultCredential(ctx, orgID, &credentialID); err != nil {
			return err
		}
	}
	return nil
}

// validateVaultCredential refuses anything but a vault_password in the
// repository's vault slot. The kinds are not interchangeable and the
// failure of a wrong one is remote from its cause: a git_token accepted
// here would be written out as a password file, Ansible would try to
// decrypt with it, and the operator would see "decryption failed" with
// nothing pointing back at the repository form.
func (s *Service) validateVaultCredential(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	cred, err := s.creds.Get(ctx, orgID, *id)
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return fmt.Errorf("%w: vault credential not found", ErrInvalidInput)
		}
		return fmt.Errorf("look up vault credential: %w", err)
	}
	if cred.Kind != credentials.KindVaultPassword {
		return fmt.Errorf("%w: the referenced vault credential must be a vault_password credential", ErrInvalidInput)
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
