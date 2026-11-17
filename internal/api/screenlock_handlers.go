package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/screenlock"
)

// lockPINMissing is the detail of the 409 that locking without a PIN answers.
// The SPA matches on it to open the "set a PIN, then lock" dialog, so it is
// contract rather than prose.
const lockPINMissing = "lock_pin_missing"

type lockPINInput struct {
	Body struct {
		// The shape (4 to 8 digits) is the service's rule; the length bound
		// only keeps an absurd body out of the hasher.
		PIN string `json:"pin" maxLength:"64"`
	}
}

// unlockOutput carries the 200 answer, or the 401 of the fifth wrong PIN
// together with the cleared cookie (a huma error cannot set a header).
type unlockOutput struct {
	Status    int
	SetCookie *http.Cookie `header:"Set-Cookie"`
	Body      unlockBody
}

type unlockBody struct {
	OK     bool   `json:"ok,omitempty"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// wrongPINError is the 422 of a wrong PIN: a problem document that also says
// how many attempts the session has left.
type wrongPINError struct {
	Status       int    `json:"status"`
	Title        string `json:"title"`
	Detail       string `json:"detail"`
	AttemptsLeft int    `json:"attempts_left"`
}

func (e *wrongPINError) Error() string  { return e.Detail }
func (e *wrongPINError) GetStatus() int { return e.Status }
func (e *wrongPINError) ContentType(string) string {
	return "application/problem+json"
}

func mapLockErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, screenlock.ErrInvalidPIN):
		return huma.Error422UnprocessableEntity("PIN must be 4 to 8 digits")
	case errors.Is(err, screenlock.ErrNoPIN):
		return huma.Error409Conflict(lockPINMissing)
	case errors.Is(err, screenlock.ErrNotLocked):
		return huma.Error409Conflict("session is not locked")
	case errors.Is(err, screenlock.ErrNotFound):
		return huma.Error401Unauthorized("login required")
	default:
		return mapAuthErr(ctx, err)
	}
}

// registerScreenLockRoutes wires the session lock (backlog 3.93). Cookie
// sessions only: a bearer token has no session to lock and no PIN to guard.
func registerScreenLockRoutes(hapi huma.API, deps Deps) {
	huma.Put(hapi, "/api/v1/auth/lock/pin", func(ctx context.Context, in *lockPINInput) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		st, err := deps.ScreenLock.Status(ctx, p.UserID)
		if err != nil {
			return nil, mapLockErr(ctx, err)
		}
		// The first PIN is set in the middle of "lock now", on a session that
		// may be hours old; replacing one is an account-security change.
		if st.PINSet {
			if err := requireFreshFactor(ctx, deps, p); err != nil {
				return nil, err
			}
		}
		if err := deps.ScreenLock.SetPIN(ctx, p.UserID, in.Body.PIN); err != nil {
			return nil, mapLockErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "set the lock PIN")
		return &okOutput{}, nil
	}, withAudit("setLockPin", "user"))

	huma.Delete(hapi, "/api/v1/auth/lock/pin", func(ctx context.Context, _ *struct{}) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := requireFreshFactor(ctx, deps, p); err != nil {
			return nil, err
		}
		if err := deps.ScreenLock.RemovePIN(ctx, p.UserID); err != nil {
			return nil, mapLockErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "removed the lock PIN")
		return &okOutput{}, nil
	}, withAudit("removeLockPin", "user"))

	huma.Post(hapi, "/api/v1/auth/lock/prompt-skipped", func(ctx context.Context, _ *struct{}) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := deps.ScreenLock.SkipPrompt(ctx, p.UserID); err != nil {
			return nil, mapLockErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.UserID.String(), "skipped the lock PIN step")
		return &okOutput{}, nil
	}, withAudit("skipLockPinPrompt", "user"))

	huma.Post(hapi, "/api/v1/auth/lock", func(ctx context.Context, _ *struct{}) (*okOutput, error) {
		p, err := requireInteractiveSession(ctx)
		if err != nil {
			return nil, err
		}
		if err := deps.ScreenLock.Lock(ctx, p.UserID, p.SessionID); err != nil {
			return nil, mapLockErr(ctx, err)
		}
		setAuditScope(ctx, nil, p.UserID)
		setAuditObject(ctx, p.SessionID.String(), "locked the session")
		return &okOutput{}, nil
	}, withAudit("lockSession", "session"))

	// The one route a locked session may call besides /auth/me and logout, so
	// it sits on requireSession, not behind refuseWhileLocked. Every attempt
	// is also charged to the auth rate limiter.
	huma.Post(hapi, "/api/v1/auth/lock/unlock", func(ctx context.Context, in *lockPINInput) (*unlockOutput, error) {
		p, err := requireSession(ctx)
		if err != nil {
			return nil, err
		}
		if p.TokenID != nil {
			return nil, huma.Error403Forbidden("interactive session required")
		}
		left, err := deps.ScreenLock.Unlock(ctx, p.UserID, p.SessionID, in.Body.PIN)
		switch {
		case err == nil:
			setAuditScope(ctx, nil, p.UserID)
			setAuditObject(ctx, p.SessionID.String(), "unlocked the session")
			return &unlockOutput{Status: http.StatusOK, Body: unlockBody{OK: true}}, nil
		case errors.Is(err, screenlock.ErrWrongPIN):
			recordInstanceAuditFailure(ctx, deps, p.UserID, "unlockSessionFailed", "session", p.SessionID.String(),
				"wrong PIN, "+strconv.Itoa(left)+" attempts left")
			return nil, &wrongPINError{Status: http.StatusUnprocessableEntity, Title: http.StatusText(http.StatusUnprocessableEntity),
				Detail: "wrong pin", AttemptsLeft: left}
		case errors.Is(err, screenlock.ErrExhausted):
			recordInstanceAuditFailure(ctx, deps, p.UserID, "unlockSessionExhausted", "session", p.SessionID.String(),
				"too many wrong PINs, session ended")
			clear := sessionCookie("", deps.Secure)
			clear.MaxAge = -1
			return &unlockOutput{Status: http.StatusUnauthorized, SetCookie: &clear,
				Body: unlockBody{Title: http.StatusText(http.StatusUnauthorized), Detail: "too many wrong pins"}}, nil
		default:
			return nil, mapLockErr(ctx, err)
		}
	}, withAuthRateLimit(deps), withAudit("unlockSession", "session"))
}
