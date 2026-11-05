-- name: CreateSession :exec
-- A session created already MFA-complete is one whose factor was just
-- proven — the passkey login path creates exactly that — so it carries
-- the proof's timestamp from the start (backlog 1.28). Without it a
-- passkey user would never hold a fresh factor and could never reach the
-- account-security actions at all.
-- A session created complete is also a completed sign-in, so it stamps
-- users.last_sign_in_at in the same statement (backlog 7.74); a pending one
-- stamps nothing until MarkSessionMFAComplete completes it.
WITH new_session AS (
    INSERT INTO sessions (id, user_id, secret_hash, mfa_complete, expires_at, mfa_at)
    VALUES ($1, $2, $3, $4, $5, CASE WHEN $4 THEN now() ELSE NULL END)
    RETURNING user_id, mfa_complete
)
UPDATE users SET last_sign_in_at = now()
FROM new_session
WHERE users.id = new_session.user_id AND new_session.mfa_complete;

-- name: GetSession :one
-- u.disabled_at IS NULL is the backstop for deactivation (Finding 2, Task
-- 10 fix round 1): DeactivateUser also revokes the user's sessions
-- explicitly, but this clause is what makes it impossible for any current
-- or future caller of Sessions.Lookup to forget the check — a disabled
-- user's session simply stops resolving here, the same way an expired or
-- revoked one already does.
SELECT s.*, u.instance_admin, u.must_change_password FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.revoked_at IS NULL AND s.expires_at > now() AND u.disabled_at IS NULL;

-- name: MarkSessionMFAComplete :execrows
-- :execrows, not :exec — completing the second factor is a security state
-- change, and an id that matches nothing must reach the caller as a
-- failure rather than as a silent success (backlog 1.26).
-- mfa_at is stamped on every call, not only the first: the same endpoints
-- that complete a pending session are what an already-complete one
-- re-proves its factor through, and that re-proof is the whole point of
-- the freshness window (backlog 1.28).
-- secret_hash is replaced too (backlog 7.19): the cookie a session had
-- before its second factor must not be the one that carries it afterwards.
-- last_sign_in_at is stamped only when the session was not yet complete
-- (backlog 7.74): a step-up re-proves the factor on a session that already
-- signed in and must not read as a new sign-in. The CTE reads the row as it
-- was before this statement's own update, and locks it (backlog 7.82): a
-- concurrent completion of the same session makes this one wait, then read
-- the session as complete and stamp nothing. The update names the row
-- through the CTE so the lock is taken first; left to itself the update ran
-- first, and the CTE's FOR UPDATE then skipped a row this statement had
-- already changed and read nothing at all.
WITH previous AS (
    SELECT s.id, s.user_id, s.mfa_complete FROM sessions s WHERE s.id = sqlc.arg(id) FOR UPDATE
), stamp AS (
    UPDATE users SET last_sign_in_at = now()
    WHERE users.id IN (SELECT user_id FROM previous WHERE NOT mfa_complete)
)
UPDATE sessions SET mfa_complete = true, mfa_at = now(), secret_hash = sqlc.arg(secret_hash)
WHERE sessions.id = (SELECT previous.id FROM previous);

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
-- The auth reaper's session pass (backlog 1.21, auth.Reaper). GetSession's
-- expires_at > now() clause already makes an expired row unauthenticated
-- at read time — the reaper exists to bound the table's growth, not for
-- correctness — so a row is retained for a 7-day grace past its expiry
-- (auth.SessionRetentionGrace: a fixed forensic window, not a security
-- margin). revoked_at plays no role: an early revoke does not earn an
-- earlier deletion, the natural expiry bounds the row's life at
-- created + SessionTTL + SessionRetentionGrace. expired_before arrives as
-- now - SessionRetentionGrace, computed by the reaper from its own
-- injected clock so the predicate (and its boundary) is testable.
DELETE FROM sessions WHERE expires_at < @expired_before::timestamptz;

-- name: DeleteExpiredResetTokens :execrows
-- The auth reaper's password reset link pass (backlog 7.74). A link stops
-- working at expires_at whether it was used or not, so the row only bounds
-- the table's growth from then on; it keeps the sessions' 7-day grace
-- (auth.SessionRetentionGrace) for the same forensic reason, and
-- expired_before arrives as now - SessionRetentionGrace.
DELETE FROM password_reset_tokens WHERE expires_at < @expired_before::timestamptz;

-- name: UpsertTOTPSecret :exec
-- The enrolling secret goes into the pending columns; a confirmed secret
-- already in secret_sealed is left exactly as it is, so an abandoned
-- re-enrolment takes nothing away (backlog 1.27). A first enrolment has
-- nothing to keep: the same bytes seed secret_sealed too, and
-- confirmed_at stays NULL, which is what makes it unusable until
-- PromoteTOTPSecret runs.
INSERT INTO totp_secrets (user_id, secret_sealed, pending_secret_sealed, pending_created_at)
VALUES ($1, $2, $2, now())
ON CONFLICT (user_id) DO UPDATE
SET pending_secret_sealed = EXCLUDED.pending_secret_sealed,
    pending_created_at = now(),
    -- A row that was never confirmed carries no factor worth keeping, so
    -- its stale secret_sealed follows the pending one; a confirmed row's
    -- does not move until the new secret is proven.
    secret_sealed = CASE WHEN totp_secrets.confirmed_at IS NULL THEN EXCLUDED.pending_secret_sealed ELSE totp_secrets.secret_sealed END;

-- name: GetTOTPSecret :one
SELECT * FROM totp_secrets WHERE user_id = $1;

-- name: ConfirmTOTP :execrows
-- Promotes the pending secret to the confirmed one. last_used_step is
-- deliberately NOT reset: the code that just proved this secret claimed
-- its step, and the step counter is secret-independent — resetting it
-- would let that very code be replayed as a login within the same window.
-- 0 rows means there was nothing in flight to confirm.
UPDATE totp_secrets
SET secret_sealed = pending_secret_sealed,
    confirmed_at = now(),
    pending_secret_sealed = NULL,
    pending_created_at = NULL
WHERE user_id = $1 AND pending_secret_sealed IS NOT NULL;

-- name: SetTOTPLastStep :execrows
UPDATE totp_secrets SET last_used_step = $2 WHERE user_id = $1 AND last_used_step < $2;

-- name: ClaimFactorAttempt :execrows
-- Backlog 7.26: spends one second-factor attempt before the code is
-- checked, unless the account is locked. SQLTOTPStore.ClaimFactorAttempt
-- carries the whole argument.
UPDATE totp_secrets
SET failed_factor_attempts = failed_factor_attempts + 1,
    factor_locked_until = CASE
        WHEN failed_factor_attempts + 1 >= sqlc.arg(free_attempts)::int
        THEN sqlc.arg(now)::timestamptz + interval '1 second' * LEAST(
            sqlc.arg(base_secs)::float8 * power(2, LEAST(failed_factor_attempts + 1 - sqlc.arg(free_attempts)::int, 30)),
            sqlc.arg(max_secs)::float8)
        ELSE factor_locked_until
    END
WHERE user_id = sqlc.arg(user_id)
  AND (factor_locked_until IS NULL OR factor_locked_until <= sqlc.arg(now)::timestamptz);

-- name: ClearFactorAttempts :exec
UPDATE totp_secrets SET failed_factor_attempts = 0, factor_locked_until = NULL WHERE user_id = $1;

-- name: DeleteTOTP :exec
DELETE FROM totp_secrets WHERE user_id = $1;

-- name: ReplaceRecoveryCodes :exec
DELETE FROM recovery_codes WHERE user_id = $1;

-- name: InsertRecoveryCode :exec
INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = now() WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
-- Backlog 1.19/1.34: how many codes the account still has. Spent codes
-- are kept rather than deleted (UseRecoveryCode stamps used_at), so the
-- count must exclude them explicitly — a row's existence is not evidence
-- it can still be redeemed.
SELECT count(*) FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- name: ClearAbandonedTOTPEnrolments :execrows
-- Backlog 1.35: a re-enrolment that was begun and never finished leaves a
-- sealed pending secret behind for good. Only the pending pair is cleared
-- — secret_sealed and confirmed_at are the factor the account is actually
-- using, and they live on the same row. Strictly less than the cutoff, so
-- a row aged exactly to it survives, matching the ceremony sweep.
UPDATE totp_secrets
SET pending_secret_sealed = NULL, pending_created_at = NULL
WHERE pending_created_at IS NOT NULL AND pending_created_at < @started_before::timestamptz;
