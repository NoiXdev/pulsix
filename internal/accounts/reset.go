package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/mail"
)

// ResetTokenTTL is how long a reset link works.
const ResetTokenTTL = 60 * time.Minute

// The per-address limit of a forgotten password: an account that was
// issued resetRequestLimit tokens within resetRequestWindow gets no more
// until the oldest of them falls out of it. The forgot route limits each
// client address on top of this.
const (
	resetRequestLimit  = 3
	resetRequestWindow = time.Hour
)

// The bounds of the background work a forgot-password request starts. The
// route is unauthenticated, so what it can keep busy is capped: at most
// maxResetJobs requests are in flight, and one that is still running after
// resetJobTimeout (a hung database or mail server) is cancelled.
const (
	maxResetJobs    = 16
	resetJobTimeout = 60 * time.Second
)

// resetTokenBytes is the randomness in one reset token: 256 bits.
const resetTokenBytes = 32

// ResetAvailable reports whether a person can reset a forgotten password
// themselves: whether the instance can send mail.
func (s *Service) ResetAvailable(ctx context.Context) (bool, error) {
	return s.mailer.Configured(ctx)
}

// RequestReset mails a reset link to the account with this address. The
// caller learns nothing from it, not even from how long it takes: past the
// check whether the instance has outbound mail at all (the same for every
// address), the whole request — finding the account, issuing the token,
// sending the mail — runs in the background, detached from the caller, and
// RequestReset returns nil at once. An error comes back only from that
// first check. What the background work does is requestReset's.
func (s *Service) RequestReset(ctx context.Context, email string) error {
	configured, err := s.mailer.Configured(ctx)
	if err != nil {
		return fmt.Errorf("check outgoing mail: %w", err)
	}
	if !configured {
		return nil
	}
	// The caller hears 202 whatever happens below: a drop must not be
	// observable. The logs carry no address.
	if !s.startBackground() {
		s.logger().WarnContext(ctx, "password reset request dropped: shutting down")
		return nil
	}
	select {
	case s.resetSlots <- struct{}{}:
	default:
		s.background.Done()
		s.logger().WarnContext(ctx, "password reset request dropped: too many in flight", "limit", maxResetJobs)
		return nil
	}
	go func() {
		defer s.background.Done()
		defer func() { <-s.resetSlots }()
		jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetJobTimeout)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				// The kind of the panic, not its value: a value built from
				// the request could carry the address.
				s.logger().ErrorContext(jobCtx, "password reset request panicked", "panic", fmt.Sprintf("%T", r), "stack", string(debug.Stack()))
			}
		}()
		if err := s.requestReset(jobCtx, email); err != nil {
			// Not the address: the log must not tell which ones were asked
			// about any more than the answer does.
			s.logger().ErrorContext(jobCtx, "password reset request failed", "error", err)
		}
	}()
	return nil
}

// startBackground counts one more piece of background work, unless
// Shutdown has begun.
func (s *Service) startBackground() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.background.Add(1)
	return true
}

// Wait blocks until the work RequestReset handed to the background has
// finished. It is for tests, which start no more work while it waits; a
// process shutting down uses Shutdown.
func (s *Service) Wait() { s.background.Wait() }

// Shutdown stops RequestReset from starting background work (a request
// from then on is dropped, and still answered as usual) and waits until
// the work already started has finished, or ctx is done, whichever comes
// first; then it returns ctx's error. Work still running past that keeps
// its own deadline (resetJobTimeout). Call it once the HTTP server has
// drained, so no reset link an accepted request promised is lost.
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.background.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for password reset requests: %w", ctx.Err())
	}
}

// requestReset is RequestReset's background work. No link is sent when no
// account has the address, when the account is deactivated or its
// invitation is still pending (AccountByEmail finds those; the locked read
// sorts them out), when it must change its password (an administrator set
// a temporary one to lock someone out, and a link would let whoever reads
// the mailbox undo that; the administrator's own SendResetLink still
// works), or when the account was already issued
// resetRequestLimit links within resetRequestWindow. Otherwise a new token
// voids the account's older ones and is mailed; a failed send is logged
// without the link. The error is a store failure, nil in every other case.
func (s *Service) requestReset(ctx context.Context, email string) error {
	userID, err := s.store.AccountByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find account: %w", err)
	}

	var token string
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		acct, err := s.store.LockAccount(ctx, userID)
		if err != nil {
			return fmt.Errorf("lock account: %w", err)
		}
		if !acct.active() || acct.MustChangePassword {
			return nil
		}
		now := s.now()
		n, err := s.store.CountResetTokensSince(ctx, userID, now.Add(-resetRequestWindow))
		if err != nil {
			return fmt.Errorf("count reset tokens: %w", err)
		}
		if n >= resetRequestLimit {
			return nil
		}
		token, err = s.issueResetToken(ctx, userID, now)
		return err
	})
	if err != nil || token == "" {
		return err
	}
	to, err := s.store.NoticeRecipient(ctx, userID)
	if err != nil {
		return fmt.Errorf("read recipient: %w", err)
	}
	_ = s.mailResetLink(ctx, userID, to, token)
	return nil
}

// SendResetLink is an instance admin mailing target a reset link, and
// reports how that went (Notice). The common checks (checkActor,
// withAccount) apply, and without outbound mail it is
// ErrMailNotConfigured. Otherwise it is requestReset without the
// per-address limit: a new link voids the older ones and is mailed
// (NoticeSent, or NoticeFailed with the failure logged without the link);
// a deactivated account gets no token at all (NoticeSkipped). The send is
// detached from the request, so a browser that goes away does not cancel
// it once the token is committed. The account's current password keeps
// working until the link is used.
func (s *Service) SendResetLink(ctx context.Context, actorIsAdmin bool, actor, target uuid.UUID) (Notice, error) {
	if err := checkActor(actorIsAdmin, actor, target); err != nil {
		return "", err
	}
	configured, err := s.mailer.Configured(ctx)
	if err != nil {
		return "", fmt.Errorf("check outgoing mail: %w", err)
	}
	if !configured {
		return "", ErrMailNotConfigured
	}
	var token string
	err = s.withAccount(ctx, target, func(ctx context.Context, acct LockedAccount) error {
		if !acct.active() {
			return nil
		}
		var err error
		token, err = s.issueResetToken(ctx, target, s.now())
		return err
	})
	if err != nil {
		return "", err
	}
	if token == "" {
		return NoticeSkipped, nil
	}
	ctx = context.WithoutCancel(ctx)
	to, err := s.store.NoticeRecipient(ctx, target)
	if err != nil {
		s.logger().WarnContext(ctx, "reset link not sent", "user_id", target.String(), "error", fmt.Errorf("read recipient: %w", err))
		return NoticeFailed, nil
	}
	switch err := s.mailResetLink(ctx, target, to, token); {
	case err == nil:
		return NoticeSent, nil
	case errors.Is(err, mail.ErrNotConfigured):
		// The settings went away between the check and the send.
		return NoticeNotConfigured, nil
	default:
		return NoticeFailed, nil
	}
}

// ResetPassword sets a new password with a reset token and returns whose
// account it was. The token must be unused and unexpired and its account
// not deactivated (ErrInvalidResetToken otherwise), and next must pass
// auth.CheckPasswordPolicy; a refused password leaves the token as it was.
// In one transaction it stores next, ends a forced change, uses up every
// token of the account and revokes every session of it. It never signs
// anyone in: the next sign-in takes the new password and the second
// factor.
//
// The token only names its account, unlocked, before anything is locked;
// an unknown one costs nothing more. The policy check and the slow hashing
// come next, still without a lock, so the account's row is held only for
// the writes. Locks are then taken in the one order every reset path uses
// — the account's row, then its token rows — so a reset and a concurrent
// request for a new link cannot deadlock, and the token is checked again
// once the account is locked.
func (s *Service) ResetPassword(ctx context.Context, token, next string) (uuid.UUID, error) {
	sum := sha256.Sum256([]byte(token))
	id, err := s.store.ResetTokenOwner(ctx, sum[:])
	if errors.Is(err, ErrNotFound) {
		return uuid.Nil, ErrInvalidResetToken
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find reset token: %w", err)
	}
	if err := auth.CheckPasswordPolicy(next); err != nil {
		return uuid.Nil, err
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return uuid.Nil, fmt.Errorf("hash password: %w", err)
	}
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		acct, err := s.store.LockAccount(ctx, id)
		if errors.Is(err, ErrNotFound) {
			// The account went since the token named it.
			return ErrInvalidResetToken
		}
		if err != nil {
			return fmt.Errorf("lock account: %w", err)
		}
		if !acct.active() {
			return ErrInvalidResetToken
		}
		now := s.now()
		err = s.store.LockResetToken(ctx, sum[:], id, now)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidResetToken
		}
		if err != nil {
			return fmt.Errorf("lock reset token: %w", err)
		}
		if err := s.store.SetPassword(ctx, id, hash, true); err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if err := s.store.VoidResetTokens(ctx, id, now); err != nil {
			return fmt.Errorf("void reset tokens: %w", err)
		}
		if err := s.store.RevokeAllSessions(ctx, id); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// issueResetToken voids userID's unused tokens and stores a new one,
// created at now and expiring ResetTokenTTL later, by its SHA-256. It
// returns the token, base64url without padding, for the link.
func (s *Service) issueResetToken(ctx context.Context, userID uuid.UUID, now time.Time) (string, error) {
	secret := make([]byte, resetTokenBytes)
	if _, err := io.ReadFull(s.rnd, secret); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("reset token id: %w", err)
	}
	if err := s.store.VoidResetTokens(ctx, userID, now); err != nil {
		return "", fmt.Errorf("void reset tokens: %w", err)
	}
	if err := s.store.InsertResetToken(ctx, ResetToken{
		ID: id, UserID: userID, Hash: sum[:], CreatedAt: now, ExpiresAt: now.Add(ResetTokenTTL),
	}); err != nil {
		return "", fmt.Errorf("insert reset token: %w", err)
	}
	return token, nil
}

// mailResetLink sends the reset link to the account's own address, in its
// effective language. The token travels in the URL fragment, which a
// browser never sends to a server, so it reaches no request log. A failure
// is logged with the account only, never the link, and returned.
func (s *Service) mailResetLink(ctx context.Context, userID uuid.UUID, to Recipient, token string) error {
	link := strings.TrimRight(s.publicURL, "/") + "/reset-password#token=" + token
	subject, body := compose(to, noticeResetLink, link)
	sendCtx, cancel := context.WithTimeout(ctx, noticeSendTimeout)
	defer cancel()
	if err := s.mailer.Send(sendCtx, to.Email, subject, body); err != nil {
		s.logger().WarnContext(ctx, "reset link not sent", "user_id", userID.String(), "error", err)
		return err
	}
	return nil
}
