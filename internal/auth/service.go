package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// User is the service-level view of a user row.
type User struct {
	ID            uuid.UUID
	Email         string
	DisplayName   string
	PasswordHash  *string
	InstanceAdmin bool
}

// Sentinel errors mapped to HTTP status by the transport layer.
var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// Store is what the auth service needs from persistence.
type Store interface {
	GetUserByEmail(ctx context.Context, email string) (User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (User, error)
}

// SessionStore is the subset of *Sessions the service uses (fakeable in tests).
type SessionStore interface {
	Create(ctx context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}

// MFAChecker reports whether a user must complete a second factor.
type MFAChecker interface {
	Enabled(ctx context.Context, userID uuid.UUID) (bool, error)
}

// NoMFA is the checker used before TOTP exists.
type NoMFA struct{}

// Enabled always returns false.
func (NoMFA) Enabled(context.Context, uuid.UUID) (bool, error) { return false, nil }

// Service implements login, logout and "who am I".
type Service struct {
	store    Store
	sessions SessionStore
	mfa      MFAChecker
}

// NewService wires the dependencies.
func NewService(store Store, sessions SessionStore, mfa MFAChecker) *Service {
	return &Service{store: store, sessions: sessions, mfa: mfa}
}

// LoginResult is what the transport needs to answer a login.
type LoginResult struct {
	Cookie      string
	MFARequired bool
}

// Login verifies the password and opens a session.
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || u.PasswordHash == nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(*u.PasswordHash, password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return LoginResult{}, ErrInvalidCredentials
	}
	mfaRequired, err := s.mfa.Enabled(ctx, u.ID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("check mfa: %w", err)
	}
	_, cookie, err := s.sessions.Create(ctx, u.ID, !mfaRequired)
	if err != nil {
		return LoginResult{}, fmt.Errorf("create session: %w", err)
	}
	return LoginResult{Cookie: cookie, MFARequired: mfaRequired}, nil
}

// Logout revokes the caller's session.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	return s.sessions.Revoke(ctx, p.SessionID)
}

// Me returns the caller's user.
func (s *Service) Me(ctx context.Context, p Principal) (User, error) {
	return s.store.GetUserByID(ctx, p.UserID)
}
