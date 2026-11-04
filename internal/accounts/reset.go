package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
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

// resetTokenBytes is the randomness in one reset token: 256 bits.
const resetTokenBytes = 32

// ResetAvailable reports whether a person can reset a forgotten password
// themselves: whether the instance can send mail.
func (s *Service) ResetAvailable(ctx context.Context) (bool, error) {
	return s.mailer.Configured(ctx)
}

// RequestReset mails a reset link to the account with this address. The
// caller learns nothing from it: it returns nil whether or not a link was
// sent, and an error only when the store fails. No link is sent when the
// instance has no outbound mail, when no account has the address, when the
// account is deactivated or its invitation is still pending, or when the
// account was already issued resetRequestLimit links within
// resetRequestWindow.
//
// A new link voids the account's older ones. The mail is handed to the
// mailer in the background, so how long the answer takes does not depend
// on the mail server; a failed send is logged without the link.
func (s *Service) RequestReset(ctx context.Context, email string) error {
	configured, err := s.mailer.Configured(ctx)
	if err != nil {
		return fmt.Errorf("check outgoing mail: %w", err)
	}
	if !configured {
		return nil
	}
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
		if !acct.active() {
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
	s.mails.Add(1)
	go func() {
		defer s.mails.Done()
		s.mailResetLink(context.WithoutCancel(ctx), userID, to, token)
	}()
	return nil
}

// SendResetLink is an instance admin mailing target a reset link. The
// common checks (checkActor, withAccount) apply, and without outbound mail
// it is ErrMailNotConfigured. Otherwise it is RequestReset without the
// per-address limit: a deactivated account gets nothing, a new link voids
// the older ones, and a failed send is logged without the link. The
// account's current password keeps working until the link is used.
func (s *Service) SendResetLink(ctx context.Context, actorIsAdmin bool, actor, target uuid.UUID) error {
	if err := checkActor(actorIsAdmin, actor, target); err != nil {
		return err
	}
	configured, err := s.mailer.Configured(ctx)
	if err != nil {
		return fmt.Errorf("check outgoing mail: %w", err)
	}
	if !configured {
		return ErrMailNotConfigured
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
	if err != nil || token == "" {
		return err
	}
	to, err := s.store.NoticeRecipient(ctx, target)
	if err != nil {
		return fmt.Errorf("read recipient: %w", err)
	}
	s.mailResetLink(ctx, target, to, token)
	return nil
}

// ResetPassword sets a new password with a reset token and returns whose
// account it was. The token must be unused and unexpired and its account
// not deactivated (ErrInvalidResetToken otherwise), and next must pass
// auth.CheckPasswordPolicy; a refused password leaves the token as it was.
// In one transaction it stores next, ends a forced change, uses up every
// token of the account and revokes every session of it. It never signs
// anyone in: the next sign-in takes the new password and the second
// factor.
func (s *Service) ResetPassword(ctx context.Context, token, next string) (uuid.UUID, error) {
	sum := sha256.Sum256([]byte(token))
	var userID uuid.UUID
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		now := s.now()
		id, err := s.store.LockResetToken(ctx, sum[:], now)
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidResetToken
		}
		if err != nil {
			return fmt.Errorf("lock reset token: %w", err)
		}
		if err := auth.CheckPasswordPolicy(next); err != nil {
			return err
		}
		hash, err := auth.HashPassword(next)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
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
		userID = id
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
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
// is logged with the account only, never the link.
func (s *Service) mailResetLink(ctx context.Context, userID uuid.UUID, to Recipient, token string) {
	link := strings.TrimRight(s.publicURL, "/") + "/reset-password#token=" + token
	subject, body := compose(to, noticeResetLink, link)
	sendCtx, cancel := context.WithTimeout(ctx, noticeSendTimeout)
	defer cancel()
	if err := s.mailer.Send(sendCtx, to.Email, subject, body); err != nil {
		slog.WarnContext(ctx, "reset link not sent", "user_id", userID.String(), "error", err)
	}
}
