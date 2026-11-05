package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrNotFound  = errors.New("not found")
	ErrNotMember = errors.New("not a member")
	ErrForbidden = errors.New("forbidden")
	ErrLastOwner = errors.New("organization needs at least one owner")
	ErrSlugTaken = errors.New("slug already in use")
	// ErrLastInstanceAdmin means the change would leave the instance with
	// no active instance administrator — refused, since first-run setup is
	// gated on none existing and could not be re-run to recover.
	ErrLastInstanceAdmin = errors.New("the instance needs at least one active instance administrator")
	// ErrInvalidInput is wrapped with a human-readable reason (see
	// ValidateOrgName); callers present err.Error() directly.
	ErrInvalidInput = errors.New("invalid input")
	// ErrInviteDelivery means outbound mail is configured but the
	// invitation e-mail could not be delivered. The invitation is not kept:
	// Invite removes it again, so nothing is left that nobody was told
	// about.
	ErrInviteDelivery = errors.New("the invitation e-mail could not be delivered")
	// ErrSignInRequired refuses accepting an invitation for an address that
	// already has an account, unless the request is signed in as that
	// account (backlog 7.10). The invitation is not used up.
	ErrSignInRequired = errors.New("this address already has an account: sign in to it, then accept the invitation")
	// ErrNotOnOwnAccount refuses an administrator deactivating their own
	// account (backlog 7.74): for their own account they use Account
	// security, like every other account action.
	ErrNotOnOwnAccount = errors.New("not on own account")
)

// maxOrgNameLength bounds an organization's display name.
const maxOrgNameLength = 200

// ValidateOrgName rejects an organization name that is blank, longer than
// maxOrgNameLength characters, or contains a control character. The name is
// shown across the product and goes into the Subject header of every
// invitation e-mail, where a CR or LF would end that header and let the rest
// of the name inject headers of its own (the mailer encodes the subject
// defensively as well — this is the first line of defence, not the only
// one).
func ValidateOrgName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: organization name is required", ErrInvalidInput)
	}
	if len([]rune(name)) > maxOrgNameLength {
		return fmt.Errorf("%w: organization name must be at most %d characters", ErrInvalidInput, maxOrgNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: organization name must not contain control characters such as line breaks", ErrInvalidInput)
		}
	}
	return nil
}

// Org is an organization.
type Org struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
}

// OrgOverview is one organization as the instance admin's list shows it:
// whatever its members, with how many active (not deactivated) accounts it
// has and whether the admin is one.
type OrgOverview struct {
	ID            uuid.UUID
	Slug          string
	Name          string
	MemberCount   int
	CreatedAt     time.Time
	ActorIsMember bool
}

// OrgMembership is one of a user's org memberships, joined with the org's
// own slug and name so ListOrgsForUser needs no second round trip.
type OrgMembership struct {
	OrgID     uuid.UUID
	Slug      string
	Name      string
	Role      Role
	CreatedAt time.Time
}

// Member is one org's view of one of its members, for the member-list route.
type Member struct {
	UserID      uuid.UUID
	Email       string
	DisplayName string
	Role        Role
	DisabledAt  *time.Time
	CreatedAt   time.Time
}

// User is the tenancy-service view of a user row — just enough to drive
// invite acceptance, not the full auth.User.
type User struct {
	ID           uuid.UUID
	Email        string
	DisplayName  string
	PasswordHash *string
}

// Invite carries the invitation. Token is the plaintext link secret and is
// only ever populated by Invite itself — nothing reads it back out of
// storage, since only its SHA-256 hash is ever persisted (see
// Store.InsertInvite).
//
// Mailed reports whether the invitation e-mail went out. When it did not
// because the instance has no outbound mail configured, AcceptURL carries
// the acceptance link instead, for the administrator to hand over
// themselves; it is empty whenever the link was mailed.
type Invite struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Email     string
	Role      Role
	InvitedBy uuid.UUID
	ExpiresAt time.Time
	Token     string
	Mailed    bool
	AcceptURL string
}

// Store is the persistence contract for tenancy. Every method that can find
// nothing returns ErrNotFound — never a swallowed error — so callers can
// fail closed the same way auth.Store and auth.TokenStore do.
type Store interface {
	CreateOrg(ctx context.Context, o Org) error
	GetOrgBySlug(ctx context.Context, slug string) (Org, error)
	GetOrgByID(ctx context.Context, id uuid.UUID) (Org, error)
	UpdateOrg(ctx context.Context, o Org) error
	ListOrgsForUser(ctx context.Context, userID uuid.UUID) ([]OrgMembership, error)
	// ListAllOrgs lists every organization ordered by name, with whether
	// actor is a member. It runs outside any organization scope.
	ListAllOrgs(ctx context.Context, actor uuid.UUID) ([]OrgOverview, error)
	GetMembership(ctx context.Context, orgID, userID uuid.UUID) (Role, error)
	AddMembership(ctx context.Context, orgID, userID uuid.UUID, role Role) error
	UpdateMembershipRole(ctx context.Context, orgID, userID uuid.UUID, role Role) error
	DeleteMembership(ctx context.Context, orgID, userID uuid.UUID) error
	CountOwners(ctx context.Context, orgID uuid.UUID) (int, error)
	ListMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	CreateUser(ctx context.Context, u User) error
	SetUserPasswordAndName(ctx context.Context, id uuid.UUID, hash, name string) error
	// DisableUser disables id and reports whether it was active right
	// before: not yet disabled, with a password (its invitation accepted).
	// An account that is already disabled stays as it is, its first
	// deactivation kept, and is reported inactive. ErrNotFound for an
	// unknown user, and ErrLastInstanceAdmin — atomically, so two
	// concurrent calls cannot both succeed — when id is the last active
	// instance administrator.
	DisableUser(ctx context.Context, id uuid.UUID, at time.Time) (wasActive bool, err error)
	RevokeSessionsForUser(ctx context.Context, id uuid.UUID) error
	RevokeTokensForUser(ctx context.Context, id uuid.UUID) error
	InsertInvite(ctx context.Context, inv Invite, tokenHash []byte) error
	// DeleteInvite removes an invitation of orgID that was never delivered.
	DeleteInvite(ctx context.Context, orgID, id uuid.UUID) error
	// PeekInvite returns the invite TakeInvite would take — unaccepted and
	// unexpired as of now — without taking it, or ErrNotFound.
	PeekInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error)
	// TakeInvite atomically finds an invite that is unaccepted and
	// unexpired as of now and marks it accepted, returning ErrNotFound if
	// no such invite exists — whether because the token is unknown,
	// expired, or already used. It must be atomic (find-and-mark in one
	// step) so two concurrent accepts of the same token can never both
	// succeed.
	TakeInvite(ctx context.Context, tokenHash []byte, now time.Time) (Invite, error)
	// ListPendingInvites returns orgID's invitations TakeInvite would still
	// take as of now.
	ListPendingInvites(ctx context.Context, orgID uuid.UUID, now time.Time) ([]PendingInvite, error)
	// RevokeInvite deletes an unaccepted invitation of orgID, returning
	// ErrNotFound when there is none with that id.
	RevokeInvite(ctx context.Context, orgID, id uuid.UUID) error
}

// Mailer sends transactional mail. Tests use a recording fake; production
// wires an SMTP-backed implementation in Task 12. Send returns an error
// wrapping mail.ErrNotConfigured when the instance has no outbound mail
// settings at all.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// OrgCreatedHook is invoked by CreateOrg once a new organization has been
// created and given its owner membership. It exists so another service can
// provision org-scoped resources of its own — today, hosts.Service.
// EnsureDefaultZone, giving the new organization its "default" zone —
// without tenancy importing that service directly: tenancy stays the
// dependency every other domain package builds on, never the other way
// around. Wiring binds the concrete function (internal/app/app.go, and
// internal/api/apitest for tests); tenancy itself only ever sees this
// narrow func type.
//
// It is deliberately not run inside a single database transaction with the
// organization insert (see CreateOrg's own doc comment for why that is not
// straightforward here, and would not even be fully atomic for the hook's
// own writes if it were forced through anyway) — a hook is called as one
// more sequential step after the organization and its owner membership are
// already committed, and its error propagates like any other step's. The
// residual gap this leaves — a hook failure after the org row already
// exists — is closed as far as it reasonably can be without that
// transaction: EnsureDefaultZone is idempotent, so any later retry (of
// CreateOrg's caller, or a manual operator action) safely converges on
// exactly one default zone, never zero or two.
type OrgCreatedHook func(ctx context.Context, orgID uuid.UUID) error

// Service implements organizations, memberships, roles and invitations.
type Service struct {
	store      Store
	mailer     Mailer
	publicURL  string
	now        func() time.Time
	orgCreated OrgCreatedHook
}

// NewService wires the store, mailer, public URL (used to build the
// invitation link) and clock. No OrgCreatedHook is installed by default —
// see SetOrgCreatedHook.
func NewService(store Store, mailer Mailer, publicURL string, now func() time.Time) *Service {
	return &Service{store: store, mailer: mailer, publicURL: publicURL, now: now}
}

// SetOrgCreatedHook installs hook to run at the end of every future
// CreateOrg call that successfully creates an organization. It is a setter
// rather than a NewService parameter so that wiring hosts.Service (which
// needs its own store, built from the same database) can happen after
// tenancy.NewService, and so every one of this package's own tests — and
// api_test.go's testTenancy, and apitest.go's own Tenancy service before
// Task 13 — keep building a Service exactly as before, with no hook
// installed, which is a no-op CreateOrg step (see CreateOrg's nil check).
func (s *Service) SetOrgCreatedHook(hook OrgCreatedHook) { s.orgCreated = hook }

// CreateOrg creates an organization and makes ownerID its sole owner, then
// — if a hook is installed (SetOrgCreatedHook) — runs it as one more
// sequential step, exactly the shape this method's own steps already had
// before OrgCreatedHook existed: store.CreateOrg, then AddMembership, then
// the hook. See OrgCreatedHook's own doc comment for why the hook runs
// this way rather than inside one database transaction with the insert.
func (s *Service) CreateOrg(ctx context.Context, ownerID uuid.UUID, slug, name string) (Org, error) {
	if err := ValidateOrgName(name); err != nil {
		return Org{}, err
	}
	org := Org{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: name, CreatedAt: s.now()}
	if err := s.store.CreateOrg(ctx, org); err != nil {
		return Org{}, fmt.Errorf("create org: %w", err)
	}
	if err := s.store.AddMembership(ctx, org.ID, ownerID, RoleOwner); err != nil {
		return Org{}, fmt.Errorf("add owner membership: %w", err)
	}
	if s.orgCreated != nil {
		if err := s.orgCreated(ctx, org.ID); err != nil {
			return Org{}, fmt.Errorf("provision new organization: %w", err)
		}
	}
	return org, nil
}

// ListOrgsForUser lists every organization userID belongs to.
func (s *Service) ListOrgsForUser(ctx context.Context, userID uuid.UUID) ([]OrgMembership, error) {
	return s.store.ListOrgsForUser(ctx, userID)
}

// ListAllOrgs lists every organization on the instance, ordered by name, for
// an instance admin (ErrForbidden for anyone else). actor is the admin, so
// each row says whether they are a member and can open the organization.
func (s *Service) ListAllOrgs(ctx context.Context, actorIsAdmin bool, actor uuid.UUID) ([]OrgOverview, error) {
	if !actorIsAdmin {
		return nil, ErrForbidden
	}
	return s.store.ListAllOrgs(ctx, actor)
}

// GetOrgBySlug looks up an organization by its slug.
func (s *Service) GetOrgBySlug(ctx context.Context, slug string) (Org, error) {
	return s.store.GetOrgBySlug(ctx, slug)
}

// GetOrgByID looks up an organization by its id. It exists for callers that
// only hold an id — spec v0.2f decision 7's collision error (and decision
// 5's ErrInUse) names organizations by uuid.UUID, and the instance-admin
// handler that resolves those ids into a slug for its response
// (internal/api/shared_handlers.go) needs this rather than reaching into
// tenancy's own store, which handlers must never do directly.
func (s *Service) GetOrgByID(ctx context.Context, id uuid.UUID) (Org, error) {
	return s.store.GetOrgByID(ctx, id)
}

// UpdateOrg renames orgID. The slug is immutable — it appears in URLs and
// API token scopes, so changing it would silently break both.
func (s *Service) UpdateOrg(ctx context.Context, orgID uuid.UUID, name string) (Org, error) {
	if err := ValidateOrgName(name); err != nil {
		return Org{}, err
	}
	org, err := s.store.GetOrgByID(ctx, orgID)
	if err != nil {
		return Org{}, err
	}
	org.Name = name
	if err := s.store.UpdateOrg(ctx, org); err != nil {
		return Org{}, fmt.Errorf("update org: %w", err)
	}
	return org, nil
}

// MembershipOf returns userID's role in orgID, or ErrNotMember if they do
// not belong to it.
func (s *Service) MembershipOf(ctx context.Context, orgID, userID uuid.UUID) (Role, error) {
	role, err := s.store.GetMembership(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrNotMember
		}
		return "", err
	}
	return role, nil
}

// ListMembers lists every member of orgID.
func (s *Service) ListMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	return s.store.ListMembers(ctx, orgID)
}
