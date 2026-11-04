-- name: LockUserPassword :one
-- The password state of an active account, locked for the rest of the
-- transaction: a password change verifies the current password and writes
-- the new one under this lock, so a concurrent change or an administrator's
-- temporary password cannot interleave with it. A deactivated account has
-- nothing to change.
SELECT password_hash, temp_password_expires_at FROM users
WHERE id = $1 AND disabled_at IS NULL
FOR UPDATE;

-- name: SetUserPassword :execrows
-- clear_must_change ends a forced change: the flag drops and so does the
-- temporary password's expiry, which only ever applies to a password an
-- administrator set.
UPDATE users SET
  password_hash = sqlc.arg(password_hash)::text,
  must_change_password = CASE WHEN sqlc.arg(clear_must_change)::boolean THEN false ELSE must_change_password END,
  temp_password_expires_at = CASE WHEN sqlc.arg(clear_must_change)::boolean THEN NULL ELSE temp_password_expires_at END
WHERE id = sqlc.arg(id);

-- name: RevokeUserSessionsExcept :exec
-- Ends every live session of the user but one (the caller's own). API
-- tokens are not sessions and stay as they are.
UPDATE sessions SET revoked_at = now()
WHERE user_id = sqlc.arg(user_id) AND id <> sqlc.arg(keep_id) AND revoked_at IS NULL;
