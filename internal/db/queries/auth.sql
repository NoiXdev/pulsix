-- name: CreateSession :exec
-- A session created already MFA-complete is one whose factor was just
-- proven — the passkey login path creates exactly that — so it carries
-- the proof's timestamp from the start (backlog 1.28). Without it a
-- passkey user would never hold a fresh factor and could never reach the
-- account-security actions at all.
INSERT INTO sessions (id, user_id, secret_hash, mfa_complete, expires_at, mfa_at)
VALUES ($1, $2, $3, $4, $5, CASE WHEN $4 THEN now() ELSE NULL END);

-- name: GetSession :one
-- u.disabled_at IS NULL is the backstop for deactivation (Finding 2, Task
-- 10 fix round 1): DeactivateUser also revokes the user's sessions
-- explicitly, but this clause is what makes it impossible for any current
-- or future caller of Sessions.Lookup to forget the check — a disabled
-- user's session simply stops resolving here, the same way an expired or
-- revoked one already does.
SELECT s.*, u.instance_admin FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.revoked_at IS NULL AND s.expires_at > now() AND u.disabled_at IS NULL;

-- name: MarkSessionMFAComplete :execrows
-- :execrows, not :exec — completing the second factor is a security state
-- change, and an id that matches nothing must reach the caller as a
-- failure rather than as a silent success (backlog 1.26).
-- mfa_at is stamped on every call, not only the first: the same endpoints
-- that complete a pending session are what an already-complete one
-- re-proves its factor through, and that re-proof is the whole point of
-- the freshness window (backlog 1.28).
UPDATE sessions SET mfa_complete = true, mfa_at = now() WHERE id = $1;

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
