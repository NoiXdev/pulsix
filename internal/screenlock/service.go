// Package screenlock implements the session lock with a PIN (backlog 3.93):
// a signed-in user locks their own session, and only the PIN (or a revoked
// session after five wrong tries) gets them back in.
package screenlock

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// MaxUnlockFailures is the number of wrong PINs a lock tolerates; the last one
// revokes the session.
const MaxUnlockFailures = 5

var (
	ErrInvalidPIN = errors.New("screenlock: PIN must be 4 to 8 digits")
	ErrNoPIN      = errors.New("screenlock: no PIN set")
	ErrWrongPIN   = errors.New("screenlock: wrong PIN")
	ErrExhausted  = errors.New("screenlock: unlock attempts used up")
	ErrNotLocked  = errors.New("screenlock: session is not locked")
	ErrNotFound   = errors.New("screenlock: session not found")
)

var pinPattern = regexp.MustCompile(`^[0-9]{4,8}$`)

// Status is what the SPA needs to know about a user's PIN.
type Status struct {
	PINSet bool
	// Prompt: the user has no PIN and was never offered one.
	Prompt bool
}

// SessionState is the lock-relevant slice of a session row.
type SessionState struct {
	Locked   bool
	Failures int
}

// Store is the persistence the service needs. Session methods only see live
// (unrevoked, unexpired) sessions.
type Store interface {
	// PINState returns the stored PIN hash ("" when none) and whether the
	// user was ever offered a PIN.
	PINState(ctx context.Context, userID uuid.UUID) (hash string, prompted bool, err error)
	// SetPIN stores the hash and marks the user as prompted.
	SetPIN(ctx context.Context, userID uuid.UUID, hash string) error
	// RemovePIN drops the hash and keeps the prompted mark.
	RemovePIN(ctx context.Context, userID uuid.UUID) error
	MarkPrompted(ctx context.Context, userID uuid.UUID) error
	// LockSession reports whether a live, unlocked session was locked.
	LockSession(ctx context.Context, sessionID uuid.UUID) (bool, error)
	// ClaimAttempt atomically counts one unlock attempt. ok is false when
	// the session is not locked, not live, or has used all attempts.
	ClaimAttempt(ctx context.Context, sessionID uuid.UUID) (count int, ok bool, err error)
	UnlockSession(ctx context.Context, sessionID uuid.UUID) error
	// SessionState reports found=false for a missing or non-live session.
	SessionState(ctx context.Context, sessionID uuid.UUID) (state SessionState, found bool, err error)
	RevokeSession(ctx context.Context, sessionID uuid.UUID) error
}

// Service holds the lock and PIN rules.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Status(ctx context.Context, userID uuid.UUID) (Status, error) {
	hash, prompted, err := s.store.PINState(ctx, userID)
	if err != nil {
		return Status{}, fmt.Errorf("read lock PIN: %w", err)
	}
	return Status{PINSet: hash != "", Prompt: hash == "" && !prompted}, nil
}

func (s *Service) SetPIN(ctx context.Context, userID uuid.UUID, pin string) error {
	if !pinPattern.MatchString(pin) {
		return ErrInvalidPIN
	}
	hash, err := auth.HashPassword(pin)
	if err != nil {
		return fmt.Errorf("hash lock PIN: %w", err)
	}
	if err := s.store.SetPIN(ctx, userID, hash); err != nil {
		return fmt.Errorf("store lock PIN: %w", err)
	}
	return nil
}

// RemovePIN keeps the "already offered" mark, so the sign-in step does not
// come back.
func (s *Service) RemovePIN(ctx context.Context, userID uuid.UUID) error {
	if err := s.store.RemovePIN(ctx, userID); err != nil {
		return fmt.Errorf("remove lock PIN: %w", err)
	}
	return nil
}

func (s *Service) SkipPrompt(ctx context.Context, userID uuid.UUID) error {
	if err := s.store.MarkPrompted(ctx, userID); err != nil {
		return fmt.Errorf("mark lock PIN prompted: %w", err)
	}
	return nil
}

// Lock locks the session. Locking an already locked session is a no-op.
func (s *Service) Lock(ctx context.Context, userID, sessionID uuid.UUID) error {
	hash, _, err := s.store.PINState(ctx, userID)
	if err != nil {
		return fmt.Errorf("read lock PIN: %w", err)
	}
	if hash == "" {
		return ErrNoPIN
	}
	locked, err := s.store.LockSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("lock session: %w", err)
	}
	if locked {
		return nil
	}
	_, found, err := s.store.SessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// Unlock checks the PIN against a locked session. It returns the attempts
// left after a wrong PIN. Every call on a locked session spends an attempt
// before the PIN is looked at, so a malformed PIN is no free probe.
func (s *Service) Unlock(ctx context.Context, userID, sessionID uuid.UUID, pin string) (int, error) {
	var count int
	for try := 0; ; try++ {
		c, ok, err := s.store.ClaimAttempt(ctx, sessionID)
		if err != nil {
			return 0, fmt.Errorf("claim unlock attempt: %w", err)
		}
		if ok {
			count = c
			break
		}
		retry, err := s.noClaim(ctx, sessionID)
		if !retry {
			return 0, err
		}
		if try > 0 {
			// Locked again and again between our reads: give up without
			// touching the session.
			return 0, ErrNotLocked
		}
	}

	hash, _, err := s.store.PINState(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("read lock PIN: %w", err)
	}
	if hash != "" && pinPattern.MatchString(pin) {
		match, verr := auth.VerifyPassword(hash, pin)
		if verr == nil && match {
			if err := s.store.UnlockSession(ctx, sessionID); err != nil {
				return 0, fmt.Errorf("unlock session: %w", err)
			}
			return MaxUnlockFailures, nil
		}
	}

	if count >= MaxUnlockFailures {
		if err := s.store.RevokeSession(ctx, sessionID); err != nil {
			return 0, fmt.Errorf("revoke session: %w", err)
		}
		return 0, ErrExhausted
	}
	return MaxUnlockFailures - count, ErrWrongPIN
}

// noClaim explains a ClaimAttempt without a row. retry is true when the
// session is locked with attempts left: it was locked again after the claim
// missed it, so the claim should be repeated. Only a locked session with all
// attempts used is revoked.
func (s *Service) noClaim(ctx context.Context, sessionID uuid.UUID) (retry bool, err error) {
	st, found, err := s.store.SessionState(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("read session: %w", err)
	}
	switch {
	case !found:
		return false, ErrNotFound
	case !st.Locked:
		return false, ErrNotLocked
	case st.Failures < MaxUnlockFailures:
		return true, nil
	}
	if err := s.store.RevokeSession(ctx, sessionID); err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	return false, ErrExhausted
}
