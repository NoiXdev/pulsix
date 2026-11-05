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
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/varsource"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrNameTaken    = errors.New("name already in use")
	// ErrInUse means a playbook still references the repository.
	ErrInUse = errors.New("repository is in use")
	// ErrForbidden means the request is well-formed but refused because of
	// who the resource belongs to, not because of anything the request
	// could change — spec decision 6's own case: deleting a projected
	// repository from inside an organization. No edit to the request would
	// ever make it succeed, which is what sets it apart from
	// ErrInvalidInput (a value the caller could fix) and is why it is a
	// separate sentinel rather than reusing that one. The house convention
	// (internal/tenancy.ErrForbidden, mapped to 403 by
	// internal/api/authz.go's mapTenancyErr) applies here the same way.
	ErrForbidden = errors.New("forbidden")
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
	// SharedRepoID is non-nil when this row is a PROJECTION of an
	// installation-owned definition (migration 00060, spec v0.2f decision
	// 1): shared_repos.id, not composed with org_id — the definition has no
	// org_id to compose with (that table's own doc comment explains why).
	// Name, URL, Branch and Subpath then belong to the definition and are
	// refused on write (Service.Update, spec decision 2); only
	// CredentialID and the vault identity set remain this organization's
	// own. Nil for an ordinary, organization-created repository.
	SharedRepoID *uuid.UUID
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
	// InTx runs fn inside one organization-scoped write transaction: every
	// store call fn makes with the context it is handed joins that
	// transaction, and all of them commit together or none do. Create and
	// Update need it because a repository's row and its labelled vault
	// identity set are two statements that must land as one — a failure
	// between them used to leave a repository stored with none of the
	// vault passwords its request named, which does not fail at save time
	// but at the next run, as a decryption error with nothing visibly
	// wrong with the configuration (backlog 2.95).
	InTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context) error) error
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
	// ListOrgRepoVaultIdentities is ListRepoVaultIdentities for every
	// repository of the organization in one call, keyed by repo id
	// (backlog 2.94): List calls this once instead of once per row.
	ListOrgRepoVaultIdentities(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]VaultIdentity, error)
	// LockVarSources locks the repository for a write of its variable
	// sources until the caller's transaction ends; ErrNotFound when the
	// repository is gone.
	LockVarSources(ctx context.Context, orgID, repoID uuid.UUID) error
	// ReplaceVarSources replaces the repository's variable sources record
	// and its playbook rows together (sources.go); ErrNotFound when the
	// repository is gone.
	ReplaceVarSources(ctx context.Context, orgID, repoID uuid.UUID, rec SourcesRecord, playbooks []varsource.Playbook) error
	// MarkVarSourcesUnread flags the record as not read by the last sync's
	// worker, creating an empty one when there is none; a repository gone
	// meanwhile is a no-op.
	MarkVarSourcesUnread(ctx context.Context, orgID, repoID uuid.UUID, at time.Time) error
	// GetVarSources is the repository's record; ErrNotFound without one.
	GetVarSources(ctx context.Context, orgID, repoID uuid.UUID) (SourcesRecord, error)
	// ListPlaybookSources is every playbook row of the repository, by path.
	ListPlaybookSources(ctx context.Context, orgID, repoID uuid.UUID) ([]varsource.Playbook, error)
	// GetPlaybookVarSource is the repository's record and the row of one
	// playbook path, read together: ErrNotFound without a record, a nil
	// playbook when the record has no row for the path.
	GetPlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (SourcesRecord, *varsource.Playbook, error)
}

// Service implements the repository registry.
type Service struct {
	store Store
	creds CredentialSource
	jobs  JobEnqueuer
	log   *slog.Logger
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
	var created Repo
	// One transaction for both writes: a repository and the vault
	// passwords its request named are one fact, and half of it is worse
	// than none (backlog 2.95).
	if err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		created, err = s.store.Create(ctx, Repo{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID,
			Name: in.Name, URL: in.URL, Branch: defaultBranch(in.Branch), Subpath: in.Subpath,
			CredentialID:      in.CredentialID,
			VaultCredentialID: in.VaultCredentialID,
		})
		if err != nil {
			return err
		}
		// validate above already refuses a non-nil in.VaultCredentialID
		// (backlog 2.92: the bridge that used to turn it into a
		// synthesized {label: "default"} entry here is retired), so
		// VaultIdentities is always the whole truth by this point — no
		// merge needed.
		if err := s.store.ReplaceRepoVaultIdentities(ctx, orgID, created.ID, in.VaultIdentities); err != nil {
			return fmt.Errorf("store repo vault identities: %w", err)
		}
		return nil
	}); err != nil {
		return Repo{}, err
	}
	created.VaultIdentities = in.VaultIdentities
	return created, nil
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
	// One query for the whole organization (backlog 2.94), not one per
	// row: a map miss (a repository with no vault passwords) is a nil
	// slice, the same zero value ListRepoVaultIdentities itself returns
	// for an empty set.
	byRepo, err := s.store.ListOrgRepoVaultIdentities(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list repo vault identities: %w", err)
	}
	for i := range list {
		list[i].VaultIdentities = byRepo[list[i].ID]
	}
	return list, nil
}

// Update replaces every editable field, name included (a clash with another
// repository's name is ErrNameTaken). The git server key pin is not an
// editable field: it survives the edit unless the URL changes — the pin
// pins the server the URL names, so a moved checkout is unpinned and the
// next sync re-pins whatever the new server presents (backlog 2.41).
//
// A projected repository (SharedRepoID set) refuses any attempt to change
// name, URL, branch or subpath — spec decision 2's table gives those four
// to the installation-owned definition, read-only inside every
// organization; only CredentialID and the vault identity set are this
// organization's to change here. The refusal runs after ordinary field
// validation (a malformed value is reported as that, not as "you may not
// change this") and before the store is touched, so a refused edit never
// partially writes.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateInput) (Repo, error) {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return Repo{}, err
	}
	if err := s.validate(ctx, orgID, in.CredentialID, in.VaultCredentialID, in.Name, in.URL, in.Branch, in.Subpath, in.VaultIdentities); err != nil {
		return Repo{}, err
	}
	branch := defaultBranch(in.Branch)
	if cur.SharedRepoID != nil {
		if err := refuseSharedFieldChange(cur, in.Name, in.URL, branch, in.Subpath); err != nil {
			return Repo{}, err
		}
	}
	next := cur
	next.Name, next.URL, next.Branch, next.Subpath, next.CredentialID =
		in.Name, in.URL, branch, in.Subpath, in.CredentialID
	// next.VaultCredentialID is deliberately left as cur's own value, not
	// overwritten with in.VaultCredentialID: validate above already refuses
	// a non-nil in.VaultCredentialID, so writing it through here would NULL
	// out a repository's legacy column on its very first save after this
	// fix — exactly the evidence backlog 2.93 wants kept intact until a
	// release has shipped with nothing writing it, which is also what
	// repoDTO's own VaultCredentialID comment (repo_handlers.go) promises a
	// reader: "a value written before that fix still reads back here".
	if next.URL != cur.URL {
		next.SSHHostKey, next.SSHHostPublicKey = "", ""
	}
	var updated Repo
	if err := s.store.InTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if updated, err = s.store.Update(ctx, next); err != nil {
			return err
		}
		// validate above already refuses a non-nil in.VaultCredentialID
		// (backlog 2.92), so VaultIdentities is always the whole truth
		// here — the same reasoning Create's own comment gives.
		if err := s.store.ReplaceRepoVaultIdentities(ctx, orgID, id, in.VaultIdentities); err != nil {
			return fmt.Errorf("store repo vault identities: %w", err)
		}
		return nil
	}); err != nil {
		return Repo{}, err
	}
	updated.VaultIdentities = in.VaultIdentities
	return updated, nil
}

// refuseSharedFieldChange refuses to change any of a projected repository's
// shared columns (spec decision 2: name, url, branch and subpath belong to
// the installation-owned definition and are read-only inside every
// organization). cur is the projection's current row; name, url, branch and
// subpath are the already-defaulted values Update is about to write.
func refuseSharedFieldChange(cur Repo, name, url, branch, subpath string) error {
	switch {
	case name != cur.Name:
		return fmt.Errorf("%w: name: this repository is a projection of an installation-owned definition; the installation owns its name", ErrInvalidInput)
	case url != cur.URL:
		return fmt.Errorf("%w: url: this repository is a projection of an installation-owned definition; the installation owns its url", ErrInvalidInput)
	case branch != cur.Branch:
		return fmt.Errorf("%w: branch: this repository is a projection of an installation-owned definition; the installation owns its branch", ErrInvalidInput)
	case subpath != cur.Subpath:
		return fmt.Errorf("%w: subpath: this repository is a projection of an installation-owned definition; the installation owns its subpath", ErrInvalidInput)
	}
	return nil
}

// Delete removes a repository that no playbook references; the foreign key
// is the guard (no cascade), so a referenced repository is ErrInUse.
//
// A projected repository (SharedRepoID set) cannot be deleted from inside
// an organization at all (spec decision 6): the next propagation from the
// definition would simply recreate it, so a delete here would either be
// silently undone or need a tombstone — the opt-out catalogue decision 4
// deliberately did not build. An organization that wants out of a shared
// repository leaves its bindings empty instead, which is already how an
// unconfigured projection looks.
//
// This refusal is ErrForbidden, not ErrInvalidInput: no change to the
// DELETE request's content — it carries none to change — could ever make
// it succeed, because the refusal is about who owns the row, not about
// anything the caller sent. Do not "simplify" this back to ErrInvalidInput
// alongside refuseSharedFieldChange's cases above: those are refused
// because of a value the caller could fix (omit the field, echo the
// current one); this one is refused regardless of the request's content,
// which is exactly the distinction a client needs to tell "fix your input"
// apart from "this is not yours to remove".
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	cur, err := s.store.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if cur.SharedRepoID != nil {
		return fmt.Errorf("%w: this repository is a projection of an installation-owned definition and cannot be deleted from inside an organization", ErrForbidden)
	}
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
//
// vaultCredentialID itself is refused outright whenever it is set (backlog
// 2.92): the single-password slot's write-time bridge into VaultIdentities
// (legacyVaultIdentities, now removed) is retired now that the condition
// for retiring it — a VaultIdentities editor in the product's UI — is met
// (RepoDialog.tsx's VaultIdentitiesField, 2.89's Task 5). The field stays
// on CreateInput/UpdateInput, and on the API's request body, specifically
// so a client still sending it gets this 400 naming the replacement
// instead of huma silently dropping an undeclared field — "refused with a
// reason" rather than "silently ignored" is the whole point.
func (s *Service) validate(ctx context.Context, orgID uuid.UUID, credentialID, vaultCredentialID *uuid.UUID, name, url, branch, subpath string, vaultIdentities []VaultIdentity) error {
	if vaultCredentialID != nil {
		return fmt.Errorf("%w: vault_credential_id no longer accepts a write; configure the repository's vault password through vault_identities instead", ErrInvalidInput)
	}
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
// vault_password credential (validateVaultCredential, below) — the only
// place that check still runs now that the legacy single VaultCredentialID
// slot no longer accepts a write at all (backlog 2.92).
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

// validateVaultCredential refuses anything but a vault_password credential —
// called once per entry of the labelled vault identity set (the legacy,
// single VaultCredentialID slot this used to also validate is refused
// outright before reaching here; see validate's own comment). The kinds are
// not interchangeable and the failure of a wrong one is remote from its
// cause: a git_token accepted here would be written out as a password
// file, Ansible would try to decrypt with it, and the operator would see
// "decryption failed" with nothing pointing back at the repository form.
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
