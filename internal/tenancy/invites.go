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

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/mail"
)

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
//
// The link carries the token in the URL fragment (/invite#token=…), which a
// browser never sends to the server, so no request log, proxy or referrer
// ever sees it (backlog 7.80).
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
	link := s.publicURL + "/invite#token=" + token
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
// that passes auth.CheckPasswordPolicy.
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
	if auth.CheckPasswordPolicy(password) != nil {
		return fmt.Errorf("%w: the password must be at least %d characters", ErrInvalidInput, auth.MinPasswordLength)
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
