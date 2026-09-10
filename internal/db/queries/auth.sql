-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, secret_hash, mfa_complete, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetSession :one
-- u.disabled_at IS NULL is the backstop for deactivation (Finding 2, Task
-- 10 fix round 1): DeactivateUser also revokes the user's sessions
-- explicitly, but this clause is what makes it impossible for any current
-- or future caller of Sessions.Lookup to forget the check — a disabled
-- user's session simply stops resolving here, the same way an expired or
-- revoked one already does.
SELECT s.*, u.instance_admin FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.revoked_at IS NULL AND s.expires_at > now() AND u.disabled_at IS NULL;

-- name: MarkSessionMFAComplete :exec
UPDATE sessions SET mfa_complete = true WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1;

-- name: UpsertTOTPSecret :exec
INSERT INTO totp_secrets (user_id, secret_sealed) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET secret_sealed = EXCLUDED.secret_sealed, confirmed_at = NULL, last_used_step = 0;

-- name: GetTOTPSecret :one
SELECT * FROM totp_secrets WHERE user_id = $1;

-- name: ConfirmTOTP :exec
UPDATE totp_secrets SET confirmed_at = now() WHERE user_id = $1;

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
