package accounts

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The page size of ListUsers: DefaultUserListLimit when the caller names
// none, never more than MaxUserListLimit.
const (
	DefaultUserListLimit = 50
	MaxUserListLimit     = 200
)

// AccountState is the one state the Users page shows for an account.
type AccountState string

// The account states, in the order they take precedence: an account that
// is several of these at once shows the first.
const (
	StateDeactivated        AccountState = "deactivated"
	StateInvitePending      AccountState = "invite_pending"
	StateMustChangePassword AccountState = "must_change_password"
	StateNoSecondFactor     AccountState = "no_second_factor"
	StateActive             AccountState = "active"
)

// AccountRecord is one account as the store reads it for the admin views:
// what the list shows and the facts its AccountState follows from.
type AccountRecord struct {
	ID            uuid.UUID
	Email         string
	DisplayName   string
	InstanceAdmin bool
	// HasPassword is false while the invitation that created the account
	// is still pending.
	HasPassword        bool
	MustChangePassword bool
	// HasSecondFactor is a confirmed TOTP secret or at least one passkey.
	HasSecondFactor bool
	DisabledAt      *time.Time
	LastSignInAt    *time.Time
	OrgCount        int
}

// AccountDetailRecord is AccountRecord plus what only the account page
// shows.
type AccountDetailRecord struct {
	AccountRecord
	CreatedAt             time.Time
	TempPasswordExpiresAt *time.Time
	Factors               Factors
	// ActiveSessions counts complete, unrevoked, unexpired sessions.
	ActiveSessions int
}

// Factors are an account's second factors.
type Factors struct {
	// TOTP is a confirmed authenticator app.
	TOTP              bool
	Passkeys          int
	RecoveryCodesLeft int
}

// Membership is one organization an account belongs to. ActorIsMember
// tells whether the administrator looking at it belongs to it too, and so
// can open it.
type Membership struct {
	OrgID         uuid.UUID
	Slug          string
	Name          string
	Role          string
	ActorIsMember bool
}

// UserSummary is one row of the Users page.
type UserSummary struct {
	ID            uuid.UUID
	Email         string
	DisplayName   string
	InstanceAdmin bool
	State         AccountState
	OrgCount      int
	LastSignInAt  *time.Time
}

// UserDetail is the account page.
type UserDetail struct {
	UserSummary
	CreatedAt             time.Time
	DisabledAt            *time.Time
	MustChangePassword    bool
	TempPasswordExpiresAt *time.Time
	Memberships           []Membership
	Factors               Factors
	ActiveSessions        int
	// InvitePending is an account whose invitation was never accepted, so
	// it has no password. State shows it only while the account is active;
	// a deactivated invitee shows as deactivated, and this tells that
	// reactivation is the one action it accepts.
	InvitePending bool
}

// ListUsers returns one page of every account on the instance, ordered by
// e-mail case-insensitively, and the cursor of the next page ("" after the
// last). q, trimmed, keeps the accounts whose e-mail or display name
// contains it, case-insensitively. limit defaults to DefaultUserListLimit
// and is capped at MaxUserListLimit. Instance admins only.
func (s *Service) ListUsers(ctx context.Context, actorIsAdmin bool, q, cursor string, limit int) ([]UserSummary, string, error) {
	if !actorIsAdmin {
		return nil, "", ErrForbidden
	}
	after, err := decodeUserCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	switch {
	case limit <= 0:
		limit = DefaultUserListLimit
	case limit > MaxUserListLimit:
		limit = MaxUserListLimit
	}
	// One row more than the page tells whether another page follows.
	recs, err := s.store.ListAccounts(ctx, strings.TrimSpace(q), after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list accounts: %w", err)
	}
	next := ""
	if len(recs) > limit {
		recs = recs[:limit]
		next = encodeUserCursor(recs[limit-1].Email)
	}
	out := make([]UserSummary, len(recs))
	for i, r := range recs {
		out[i] = summaryOf(r)
	}
	return out, next, nil
}

// GetUser returns the account page of id, with the memberships marked
// for actor. Instance admins only; ErrNotFound for an unknown id.
func (s *Service) GetUser(ctx context.Context, actorIsAdmin bool, actor, id uuid.UUID) (UserDetail, error) {
	if !actorIsAdmin {
		return UserDetail{}, ErrForbidden
	}
	rec, err := s.store.GetAccount(ctx, id)
	if err != nil {
		return UserDetail{}, fmt.Errorf("get account: %w", err)
	}
	memberships, err := s.store.ListMemberships(ctx, id, actor)
	if err != nil {
		return UserDetail{}, fmt.Errorf("list memberships: %w", err)
	}
	return UserDetail{
		UserSummary:           summaryOf(rec.AccountRecord),
		CreatedAt:             rec.CreatedAt,
		DisabledAt:            rec.DisabledAt,
		MustChangePassword:    rec.MustChangePassword,
		InvitePending:         !rec.HasPassword,
		TempPasswordExpiresAt: rec.TempPasswordExpiresAt,
		Memberships:           memberships,
		Factors:               rec.Factors,
		ActiveSessions:        rec.ActiveSessions,
	}, nil
}

func summaryOf(r AccountRecord) UserSummary {
	return UserSummary{
		ID: r.ID, Email: r.Email, DisplayName: r.DisplayName, InstanceAdmin: r.InstanceAdmin,
		State: stateOf(r), OrgCount: r.OrgCount, LastSignInAt: r.LastSignInAt,
	}
}

// stateOf picks the account's state; the first that applies wins.
func stateOf(r AccountRecord) AccountState {
	switch {
	case r.DisabledAt != nil:
		return StateDeactivated
	case !r.HasPassword:
		return StateInvitePending
	case r.MustChangePassword:
		return StateMustChangePassword
	case !r.HasSecondFactor:
		return StateNoSecondFactor
	default:
		return StateActive
	}
}

// The list cursor is the last address of the page, opaque to the caller.
// The address is unique and ordered case-insensitively, so it alone tells
// where the next page starts.
func encodeUserCursor(email string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(email))
}

func decodeUserCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return "", ErrInvalidCursor
	}
	return string(raw), nil
}
