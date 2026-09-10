package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

// ErrCredentialExists is returned when a credential id presented at
// registration is already bound to an account (its own or another one's).
// The unique constraint on passkeys.credential_id is what actually enforces
// this; SQLPasskeyStore.AddCredential maps the resulting unique-violation to
// this sentinel so the transport layer can answer 409 instead of a mapped
// 500 (Finding "adjacent", fix round 1).
var ErrCredentialExists = errors.New("credential already registered")

// CeremonyTTL bounds the time between begin and finish for both
// registration and login ceremonies.
const CeremonyTTL = 5 * time.Minute

// ceremonyKindRegister and ceremonyKindLogin tag a stored ceremony so
// FinishRegistration cannot be driven to completion with a ceremony that
// BeginLogin created, and vice versa.
const (
	ceremonyKindRegister = "register"
	ceremonyKindLogin    = "login"
)

// Credential is a stored passkey, independent of the go-webauthn and sqlc
// row shapes.
type Credential struct {
	ID             uuid.UUID
	Name           string
	CredentialID   []byte
	PublicKey      []byte
	Attestation    string
	Transports     []string
	SignCount      uint32
	BackupEligible bool
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

// PasskeyStore persists credentials and in-flight ceremonies.
type PasskeyStore interface {
	ListCredentials(ctx context.Context, userID uuid.UUID) ([]Credential, error)
	AddCredential(ctx context.Context, userID uuid.UUID, name string, c Credential) error
	UpdateCredentialCounter(ctx context.Context, credentialID []byte, counter uint32) error
	// DeleteCredential deletes the credential owned by userID and returns
	// ErrNotFound if no row matched (an unknown id, or one owned by a
	// different user) so the service never reports success for a deletion
	// it did not actually perform.
	DeleteCredential(ctx context.Context, userID, id uuid.UUID) error
	FindUserByCredentialID(ctx context.Context, credentialID []byte) (uuid.UUID, error)
	// PutCeremony stores an in-flight ceremony. userID is nil for a login
	// ceremony (the user is not known until the assertion arrives).
	PutCeremony(ctx context.Context, id uuid.UUID, kind string, userID *uuid.UUID, data []byte, expiresAt time.Time) error
	// TakeCeremony deletes the ceremony as part of reading it, so a
	// ceremony id can only ever be finished once. It returns ErrNotFound
	// for an unknown, already-taken, or expired ceremony.
	TakeCeremony(ctx context.Context, id uuid.UUID) (kind string, userID *uuid.UUID, data []byte, err error)
}

// Passkeys runs WebAuthn registration and login ceremonies.
type Passkeys struct {
	wa       *webauthn.WebAuthn
	store    PasskeyStore
	users    Store
	sessions SessionStore
	now      func() time.Time
}

// NewPasskeys configures go-webauthn for the given relying party. rpID is
// the relying party id (a registrable domain, no scheme or port); origin is
// the full origin (scheme + host, with port) the browser reports as the
// ceremony's collected origin. Both are derived from config.Config.PublicURL
// in production; see config.Config.RPID and config.Config.Origin.
func NewPasskeys(store PasskeyStore, users Store, sessions SessionStore, rpID, rpName, origin string, now func() time.Time) (*Passkeys, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: rpName, RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure webauthn: %w", err)
	}
	return &Passkeys{wa: wa, store: store, users: users, sessions: sessions, now: now}, nil
}

// waUser adapts our User + credentials to webauthn.User.
type waUser struct {
	u     User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return w.u.ID[:] }
func (w waUser) WebAuthnName() string                       { return w.u.Email }
func (w waUser) WebAuthnDisplayName() string                { return w.u.DisplayName }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func toWebauthnCredentials(cs []Credential) []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(cs))
	for _, c := range cs {
		tr := make([]protocol.AuthenticatorTransport, len(c.Transports))
		for i, t := range c.Transports {
			tr[i] = protocol.AuthenticatorTransport(t)
		}
		out = append(out, webauthn.Credential{
			ID: c.CredentialID, PublicKey: c.PublicKey, AttestationType: c.Attestation, Transport: tr,
			Authenticator: webauthn.Authenticator{SignCount: c.SignCount},
			Flags:         webauthn.CredentialFlags{BackupEligible: c.BackupEligible},
		})
	}
	return out
}

func fromWebauthnCredential(cred *webauthn.Credential) Credential {
	tr := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		tr[i] = string(t)
	}
	return Credential{
		CredentialID: cred.ID, PublicKey: cred.PublicKey, Attestation: cred.AttestationType, Transports: tr,
		SignCount: cred.Authenticator.SignCount, BackupEligible: cred.Flags.BackupEligible,
	}
}

func (p *Passkeys) loadUser(ctx context.Context, userID uuid.UUID) (waUser, error) {
	u, err := p.users.GetUserByID(ctx, userID)
	if err != nil {
		return waUser{}, err
	}
	creds, err := p.store.ListCredentials(ctx, userID)
	if err != nil {
		return waUser{}, err
	}
	return waUser{u: u, creds: toWebauthnCredentials(creds)}, nil
}

// saveCeremony marshals sd and stores it under a fresh ceremony id, tagged
// with kind and (for registration) the user it was begun for.
func (p *Passkeys) saveCeremony(ctx context.Context, kind string, userID *uuid.UUID, sd *webauthn.SessionData) (uuid.UUID, error) {
	data, err := json.Marshal(sd)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshal session data: %w", err)
	}
	id := uuid.Must(uuid.NewV7())
	if err := p.store.PutCeremony(ctx, id, kind, userID, data, p.now().Add(CeremonyTTL)); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// takeCeremony consumes the ceremony (single use, enforced by the store) and
// checks it matches wantKind, so a registration ceremony id can never
// complete a login and vice versa.
func (p *Passkeys) takeCeremony(ctx context.Context, id uuid.UUID, wantKind string) (*uuid.UUID, *webauthn.SessionData, error) {
	kind, userID, data, err := p.store.TakeCeremony(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if kind != wantKind {
		return nil, nil, fmt.Errorf("ceremony %s: wrong kind: %w", id, ErrNotFound)
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, nil, fmt.Errorf("unmarshal session data: %w", err)
	}
	return userID, &sd, nil
}

// BeginRegistration returns creation options for the browser and the id of
// the ceremony they belong to.
func (p *Passkeys) BeginRegistration(ctx context.Context, userID uuid.UUID) (json.RawMessage, uuid.UUID, error) {
	u, err := p.loadUser(ctx, userID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sd, err := p.wa.BeginRegistration(u, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("begin registration: %w", err)
	}
	cid, err := p.saveCeremony(ctx, ceremonyKindRegister, &userID, sd)
	if err != nil {
		return nil, uuid.Nil, err
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("marshal options: %w", err)
	}
	return raw, cid, nil
}

// FinishRegistration verifies the attestation and stores the credential.
// ceremonyID must be a registration ceremony begun by userID: it is
// rejected (as ErrNotFound) if it belongs to another user, was begun as a
// login, is unknown, or has already been consumed or expired.
func (p *Passkeys) FinishRegistration(ctx context.Context, userID, ceremonyID uuid.UUID, name string, response json.RawMessage) error {
	cUser, sd, err := p.takeCeremony(ctx, ceremonyID, ceremonyKindRegister)
	if err != nil {
		return err
	}
	if cUser == nil || *cUser != userID {
		return fmt.Errorf("ceremony %s belongs to another user: %w", ceremonyID, ErrNotFound)
	}
	u, err := p.loadUser(ctx, userID)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(response))
	if err != nil {
		return fmt.Errorf("parse attestation: %w", err)
	}
	cred, err := p.wa.CreateCredential(u, *sd, parsed)
	if err != nil {
		return fmt.Errorf("verify attestation: %w", err)
	}
	c := fromWebauthnCredential(cred)
	c.ID, c.Name = uuid.Must(uuid.NewV7()), name
	return p.store.AddCredential(ctx, userID, name, c)
}

// BeginLogin returns request options for a discoverable-credential login:
// no email or prior session is needed, and the browser is left to offer
// whichever passkey it holds for this relying party.
func (p *Passkeys) BeginLogin(ctx context.Context) (json.RawMessage, uuid.UUID, error) {
	opts, sd, err := p.wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("begin login: %w", err)
	}
	cid, err := p.saveCeremony(ctx, ceremonyKindLogin, nil, sd)
	if err != nil {
		return nil, uuid.Nil, err
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("marshal options: %w", err)
	}
	return raw, cid, nil
}

// FinishLogin verifies the assertion and opens an MFA-complete session: a
// passkey login requires user verification (enforced by the RPID's
// AuthenticatorSelection above), so it counts as a completed second factor
// on its own.
func (p *Passkeys) FinishLogin(ctx context.Context, ceremonyID uuid.UUID, response json.RawMessage) (string, error) {
	_, sd, err := p.takeCeremony(ctx, ceremonyID, ceremonyKindLogin)
	if err != nil {
		return "", err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(response))
	if err != nil {
		return "", fmt.Errorf("parse assertion: %w", err)
	}
	// handler resolves the credential's owner and, critically, checks that
	// the authenticator-reported user handle actually matches that owner's
	// user id — without this an assertion for one credential could be
	// replayed with a forged userHandle to sign in as someone else.
	//
	// infraErr records a genuine failure inside the handler (a store
	// outage), as distinct from FindUserByCredentialID's ErrNotFound or a
	// user-handle mismatch, both of which just mean "this assertion does
	// not validate". go-webauthn wraps whatever the handler returns into
	// its own generic ceremony error, so infraErr is the only way left,
	// once ValidatePasskeyLogin returns, to tell an infrastructure failure
	// apart from a genuinely bad assertion: the former must propagate as an
	// internal error (logged, 500) rather than be reported to the client as
	// "invalid credentials" the way the latter is below.
	var infraErr error
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		userID, err := p.store.FindUserByCredentialID(ctx, rawID)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				infraErr = err
			}
			return nil, err
		}
		if userHandleMismatch(userHandle, userID) {
			return nil, errors.New("user handle does not match credential owner")
		}
		u, err := p.loadUser(ctx, userID)
		if err != nil {
			// Reachable only as an infrastructure failure: FindUserByCredentialID
			// just succeeded, so the user this credential belongs to exists.
			infraErr = err
			return nil, err
		}
		return u, nil
	}
	user, cred, err := p.wa.ValidatePasskeyLogin(handler, *sd, parsed)
	if err != nil {
		if infraErr != nil {
			return "", infraErr
		}
		return "", ErrInvalidCredentials
	}
	found, ok := user.(waUser)
	if !ok {
		return "", fmt.Errorf("unexpected user type %T from ValidatePasskeyLogin", user)
	}
	if found.u.DisabledAt != nil {
		// Deactivation must end access at every entry point, not just
		// password login (Finding 2, Task 10 fix round 1): a disabled
		// account's passkey must not open a fresh session either, even
		// though the assertion itself verifies correctly against the
		// stored credential. Checked before touching the counter at all,
		// so a disabled account's passkey use leaves no
		// UpdateCredentialCounter call behind.
		return "", ErrInvalidCredentials
	}
	// go-webauthn does not fail a login when the signature counter regresses
	// or repeats: Authenticator.UpdateCounter (called inside
	// ValidatePasskeyLogin, above) only sets CloneWarning and leaves
	// SignCount unchanged in that case, then validation still reports
	// success. Left unchecked, an assertion from a cloned or rolled-back
	// authenticator would produce a normal MFA-complete session with no
	// trace anywhere. Reject it here with the same generic error a failed
	// assertion produces, log the event so an operator can find it, and —
	// deliberately — skip UpdateCredentialCounter below, so the stored
	// counter still reflects the last legitimate use rather than being
	// overwritten with the (unchanged) value UpdateCounter left in cred
	// (Finding 1, fix round 1).
	if cred.Authenticator.CloneWarning {
		slog.ErrorContext(ctx, "passkey clone signal: signature counter did not advance",
			"credential_id", base64.RawURLEncoding.EncodeToString(cred.ID))
		return "", ErrInvalidCredentials
	}
	if err := p.store.UpdateCredentialCounter(ctx, cred.ID, cred.Authenticator.SignCount); err != nil {
		return "", err
	}
	_, cookie, err := p.sessions.Create(ctx, found.u.ID, true)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return cookie, nil
}

// userHandleMismatch is a package-level indirection (mirroring
// verifyPassword's indirection in service.go) over the local, explicit
// check that FinishLogin's DiscoverableUserHandler uses to enforce that an
// authenticator-reported user handle actually matches the credential's
// resolved owner — without it, a credential's rawID together with a forged
// userHandle could be replayed to sign in as an arbitrary account.
//
// go-webauthn's own validateLogin independently re-checks the identical
// property once the handler returns, so through FinishLogin's public API
// alone the two checks are indistinguishable: removing this one still
// leaves a forged assertion rejected by the library's Step 2 check, and
// FinishLogin folds every rejection into the same generic
// ErrInvalidCredentials regardless of which layer caught it. The
// indirection lets a white-box test substitute a spy and observe that this
// call site specifically runs (TestLocalUserHandleCheckReallyRuns);
// TestUserHandleMismatchDetectsForgedHandle separately proves the
// comparison's own logic. See TestFinishLoginRejectsUserHandleMismatch in
// passkey_test.go for the black-box regression test this replaces as the
// sole evidence for this property (Finding 4, fix round 1).
var userHandleMismatch = func(userHandle []byte, owner uuid.UUID) bool {
	return !bytes.Equal(userHandle, owner[:])
}

// List returns the user's passkeys for the settings page.
func (p *Passkeys) List(ctx context.Context, userID uuid.UUID) ([]Credential, error) {
	return p.store.ListCredentials(ctx, userID)
}

// Enabled reports whether userID has at least one registered passkey, so
// *Passkeys satisfies MFAChecker: a passkey must count as a completed second
// factor at password login, the same as a confirmed TOTP secret, not only
// at the passkey login endpoints (Finding 2, fix round 1). It fails closed,
// the same as TOTP.Enabled: a store error propagates rather than being
// swallowed into (false, nil), which would otherwise let a database hiccup
// silently downgrade a passkey-protected account to password-only login.
func (p *Passkeys) Enabled(ctx context.Context, userID uuid.UUID) (bool, error) {
	creds, err := p.store.ListCredentials(ctx, userID)
	if err != nil {
		return false, err
	}
	return len(creds) > 0, nil
}

// Delete removes one of userID's passkeys, returning ErrNotFound if id does
// not name a credential owned by userID.
func (p *Passkeys) Delete(ctx context.Context, userID, id uuid.UUID) error {
	return p.store.DeleteCredential(ctx, userID, id)
}
