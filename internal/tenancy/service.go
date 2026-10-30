package tenancy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/mail"
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
)

// minInvitePasswordLen is the floor for the password of an account an
// invitation creates: a basic guard, not a full strength policy.
const minInvitePasswordLen = 8

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

// InviteTTL is how long an invitation link works.
const InviteTTL = 7 * 24 * time.Hour

// inviteSendTimeout bounds the detached invitation send. It is the mail
// package's own session timeout plus a margin: the mailer already gives up
// on a silent server after 30 s, so this is the backstop for the case
// where it somehow does not, not the primary limit.
const inviteSendTimeout = 45 * time.Second

// inviteSecretBytes is the raw entropy carried by an invite token, the same
// width Sessions.Create and Tokens.Create use for their own secrets.
const inviteSecretBytes = 24

// Org is an organization.
type Org struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
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
	GetMembership(ctx context.Context, orgID, userID uuid.UUID) (Role, error)
	AddMembership(ctx context.Context, orgID, userID uuid.UUID, role Role) error
	UpdateMembershipRole(ctx context.Context, orgID, userID uuid.UUID, role Role) error
	DeleteMembership(ctx context.Context, orgID, userID uuid.UUID) error
	CountOwners(ctx context.Context, orgID uuid.UUID) (int, error)
	ListMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	CreateUser(ctx context.Context, u User) error
	SetUserPasswordAndName(ctx context.Context, id uuid.UUID, hash, name string) error
	// DisableUser disables id, returning ErrNotFound for an unknown user and
	// ErrLastInstanceAdmin — atomically, so two concurrent calls cannot both
	// succeed — when id is the last active instance administrator.
	DisableUser(ctx context.Context, id uuid.UUID, at time.Time) error
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

// Invite creates an invitation for email to join orgID as role and e-mails
// the acceptance link. The owner role can never be granted by invite — only
// ChangeRole, and only by an existing owner, can create a second owner.
//
// Inviting does not depend on outbound mail being configured: when it is
// not (mail.ErrNotConfigured), the invitation is kept and returned with
// AcceptURL set, so the administrator can hand the link over themselves.
// When mail is configured but delivery fails, the invitation is deleted
// again — nobody was told about it, so it must not linger — and the call
// fails with ErrInviteDelivery wrapping the mailer's own error.
func (s *Service) Invite(ctx context.Context, orgID, byUserID uuid.UUID, email string, role Role) (Invite, error) {
	if !Valid(role) || role == RoleOwner {
		return Invite{}, fmt.Errorf("invite role %q: %w", role, ErrForbidden)
	}
	org, err := s.store.GetOrgByID(ctx, orgID)
	if err != nil {
		return Invite{}, err
	}
	secret := make([]byte, inviteSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return Invite{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	inv := Invite{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Email: strings.ToLower(email), Role: role,
		InvitedBy: byUserID, ExpiresAt: s.now().Add(InviteTTL), Token: token,
	}
	if err := s.store.InsertInvite(ctx, inv, sum[:]); err != nil {
		return Invite{}, fmt.Errorf("insert invite: %w", err)
	}
	link := s.publicURL + "/invite/" + token
	body := fmt.Sprintf("You have been invited to %s on Pulsix as %s.\n\nAccept: %s\n\nThe link expires in 7 days.",
		org.Name, role, link)
	// The send is deliberately not governed by the request (backlog 1.25).
	// A client that disconnects — a closed tab, a reverse proxy timing the
	// request out — would otherwise tear the SMTP conversation down
	// mid-flight, and the default branch below would delete an invitation
	// whose mail may already have been accepted. Whether someone receives
	// their invitation must not depend on whether the person who sent it
	// stayed on the page.
	//
	// Detached, not unbounded: the mailer honours whatever context it is
	// given (which is why this is done here and not inside it — the "send
	// a test e-mail" button genuinely should die with its request), so it
	// needs a deadline of its own or a black-holed server would hold this
	// call forever.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inviteSendTimeout)
	defer cancel()
	sendErr := s.mailer.Send(sendCtx, inv.Email, "Invitation to "+org.Name, body)
	switch {
	case sendErr == nil:
		inv.Mailed = true
	case errors.Is(sendErr, mail.ErrNotConfigured):
		inv.AcceptURL = link
	default:
		if err := s.store.DeleteInvite(ctx, orgID, inv.ID); err != nil {
			return Invite{}, fmt.Errorf("%w: %w (and removing the undelivered invitation failed: %w)", ErrInviteDelivery, sendErr, err)
		}
		return Invite{}, fmt.Errorf("%w: %w", ErrInviteDelivery, sendErr)
	}
	return inv, nil
}

// AcceptInvite redeems token, creating or reusing the invited user, and
// joins them to the invite's org with the invite's role. acceptor is the
// user the request is signed in as (an MFA-complete interactive session),
// uuid.Nil when none.
//
// The token proves at most that someone saw the link, not who they are: an
// instance without outbound mail hands the link to the inviter (see Invite),
// and a link can be forwarded. So it never speaks for an account that
// already has a password (backlog 7.10):
//
//   - An address whose account has its own password joins only when acceptor
//     is that account. Anything else is ErrSignInRequired, and the invitation
//     is checked first and not used up, so the account's holder can sign in
//     and accept it. The account's password and display name are never
//     touched. Without this rule, whoever redeemed an unmailed link for
//     someone else's address owned that account, and every later invitation
//     to the address, from any organization, joined it.
//   - An address with no account, or only a user row a previous, never
//     accepted invite left behind (PasswordHash == nil, nobody ever signed
//     in), gets its first password and display name here; both are required.
//
// The returned newAccount reports which case happened: true when this call
// just gave the address its first password, false when an existing account
// joined. The handler layer (see api.registerInviteAcceptRoute) opens a
// session only for a new account.
func (s *Service) AcceptInvite(ctx context.Context, acceptor uuid.UUID, token, displayName, password string) (userID uuid.UUID, orgID uuid.UUID, newAccount bool, err error) {
	sum := sha256.Sum256([]byte(token))
	inv, err := s.store.PeekInvite(ctx, sum[:], s.now())
	if err != nil {
		return uuid.Nil, uuid.Nil, false, inviteLookupErr(err)
	}
	u, err := s.store.GetUserByEmail(ctx, inv.Email)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return uuid.Nil, uuid.Nil, false, err
	}
	if err := checkAcceptor(exists, u, acceptor, displayName, password); err != nil {
		return uuid.Nil, uuid.Nil, false, err
	}
	if inv, err = s.store.TakeInvite(ctx, sum[:], s.now()); err != nil {
		return uuid.Nil, uuid.Nil, false, inviteLookupErr(err)
	}
	// Read the address again now that the invite is ours: a concurrent
	// acceptance may have created the account in between.
	u, err = s.store.GetUserByEmail(ctx, inv.Email)
	created := false
	switch {
	case errors.Is(err, ErrNotFound):
		hash, err := auth.HashPassword(password)
		if err != nil {
			return uuid.Nil, uuid.Nil, false, fmt.Errorf("hash password: %w", err)
		}
		u = User{ID: uuid.Must(uuid.NewV7()), Email: inv.Email, DisplayName: displayName, PasswordHash: &hash}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return uuid.Nil, uuid.Nil, false, fmt.Errorf("create invited user: %w", err)
		}
		created = true
	case err != nil:
		return uuid.Nil, uuid.Nil, false, err
	case u.PasswordHash == nil:
		hash, err := auth.HashPassword(password)
		if err != nil {
			return uuid.Nil, uuid.Nil, false, fmt.Errorf("hash password: %w", err)
		}
		if err := s.store.SetUserPasswordAndName(ctx, u.ID, hash, displayName); err != nil {
			return uuid.Nil, uuid.Nil, false, fmt.Errorf("set invited user credentials: %w", err)
		}
		created = true
	case u.ID != acceptor:
		// The account appeared after the first check; the invitation is
		// used up, but it still never joins an account its holder did not
		// sign in to.
		return uuid.Nil, uuid.Nil, false, ErrSignInRequired
	}
	if err := s.store.AddMembership(ctx, inv.OrgID, u.ID, inv.Role); err != nil {
		return uuid.Nil, uuid.Nil, false, fmt.Errorf("add membership: %w", err)
	}
	return u.ID, inv.OrgID, created, nil
}

// checkAcceptor decides, before an invitation is taken, whether this request
// may accept it: an address with a password-bearing account only through
// that account's own session, a new account only with a name and a password
// of minInvitePasswordLen or more.
func checkAcceptor(exists bool, u User, acceptor uuid.UUID, displayName, password string) error {
	if exists && u.PasswordHash != nil {
		if u.ID != acceptor {
			return ErrSignInRequired
		}
		return nil
	}
	if strings.TrimSpace(displayName) == "" {
		return fmt.Errorf("%w: a name is required to create the account", ErrInvalidInput)
	}
	if len(password) < minInvitePasswordLen {
		return fmt.Errorf("%w: the password must be at least %d characters", ErrInvalidInput, minInvitePasswordLen)
	}
	return nil
}

// inviteLookupErr maps a Peek/TakeInvite failure: the store already folds
// "unknown token", "already accepted" and "expired" into ErrNotFound, so
// that is a genuine no-such-invite. Anything else is a store failure and
// must not present as a silent 404 (Task 10 fix round 1).
func inviteLookupErr(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("take invite: %w", err)
}

// ChangeRole sets target's role in orgID. actorRole is the caller's already
// -authorized effective role — api.requireOrg computes it as the lower of
// the caller's real membership role and any bearer token's own scoped role,
// and it is what actually decides the owner-only rules below. It must never
// be re-derived from the store here: a token minted at, say, admin can
// authenticate as its owner (an org owner) and would resolve to RoleOwner
// on a fresh store lookup, silently discarding the cap requireOrg computed
// and letting an admin-scoped token grant or revoke ownership (Finding 1,
// Task 10 fix round 1) — exactly the bypass api/authz.go's own doc comment
// says a token must never be able to produce.
//
// Only an actor with RoleOwner may grant or revoke the owner role, and the
// organization must always keep at least one owner: demoting the last
// owner fails with ErrLastOwner, enforced atomically by the store itself
// (see Store.UpdateMembershipRole) so two concurrent demotions of the same
// two owners can never both succeed (Finding 3, Task 10 fix round 1) — this
// no longer calls CountOwners at all.
func (s *Service) ChangeRole(ctx context.Context, orgID uuid.UUID, actorRole Role, actor, target uuid.UUID, role Role) error {
	if !Valid(role) {
		return ErrForbidden
	}
	current, err := s.store.GetMembership(ctx, orgID, target)
	if err != nil {
		return err // ErrNotFound when target genuinely isn't a member; propagated unchanged otherwise
	}
	if (role == RoleOwner || current == RoleOwner) && actorRole != RoleOwner {
		return ErrForbidden
	}
	if err := s.store.UpdateMembershipRole(ctx, orgID, target, role); err != nil {
		if errors.Is(err, ErrNotFound) && current == RoleOwner && role != RoleOwner {
			// GetMembership above just confirmed target exists, so the only
			// way the guarded UPDATE can have matched no row is its own
			// atomic last-owner check.
			return ErrLastOwner
		}
		return err
	}
	return nil
}

// RemoveMember removes target from orgID, with the same actor-role and
// last-owner handling as ChangeRole: actorRole is the caller's already-
// capped effective role, never re-derived from the store; removing the
// only remaining owner fails with ErrLastOwner, enforced atomically by the
// store (Store.DeleteMembership); and only an actor with RoleOwner may
// remove another owner.
func (s *Service) RemoveMember(ctx context.Context, orgID uuid.UUID, actorRole Role, actor, target uuid.UUID) error {
	current, err := s.store.GetMembership(ctx, orgID, target)
	if err != nil {
		return err // ErrNotFound when target genuinely isn't a member; propagated unchanged otherwise
	}
	if current == RoleOwner && actorRole != RoleOwner {
		return ErrForbidden
	}
	if err := s.store.DeleteMembership(ctx, orgID, target); err != nil {
		if errors.Is(err, ErrNotFound) && current == RoleOwner {
			// Same reasoning as ChangeRole above: existence was just
			// confirmed, so a guarded delete matching no row means the
			// atomic last-owner check refused it.
			return ErrLastOwner
		}
		return err
	}
	return nil
}

// DeactivateUser disables target's account instance-wide and ends its
// access immediately: sessions and API tokens are revoked, not merely
// flagged for later. It refuses, with ErrLastInstanceAdmin and before
// revoking anything, to disable the last active instance administrator —
// the caller themselves included — since the instance could then only be
// recovered by editing the database. actorIsInstanceAdmin is supplied by the handler from
// the caller's auth.Principal — the tenancy package has no notion of
// instance-level roles of its own, only org membership, so it cannot look
// this up itself.
func (s *Service) DeactivateUser(ctx context.Context, actorIsInstanceAdmin bool, target uuid.UUID) error {
	if !actorIsInstanceAdmin {
		return ErrForbidden
	}
	if err := s.store.DisableUser(ctx, target, s.now()); err != nil {
		return fmt.Errorf("disable user: %w", err)
	}
	if err := s.store.RevokeSessionsForUser(ctx, target); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	if err := s.store.RevokeTokensForUser(ctx, target); err != nil {
		return fmt.Errorf("revoke tokens: %w", err)
	}
	return nil
}
