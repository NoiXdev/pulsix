-- name: LockUserPassword :one
-- The password state of an active account, locked for the rest of the
-- transaction: a password change verifies the current password and writes
-- the new one under this lock, so a concurrent change or an administrator's
-- temporary password cannot interleave with it. A deactivated account has
-- nothing to change.
SELECT password_hash, temp_password_expires_at FROM users
WHERE id = $1 AND disabled_at IS NULL
FOR UPDATE;

-- name: GetUserPassword :one
-- LockUserPassword without the lock: a password change verifies the
-- current password against this read before it locks the row, so the slow
-- hash comparison never holds the lock, and then re-reads under the lock
-- to make sure the hash it verified is still the one stored.
SELECT password_hash, temp_password_expires_at FROM users
WHERE id = $1 AND disabled_at IS NULL;

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

-- name: ListAccounts :many
-- One page of every account on the instance for the instance admin's Users
-- page, ordered by e-mail. email is citext, so both the order and the
-- keyset bound (after) are case-insensitive, and as the column is unique
-- the address alone resumes a page. The search is a case-insensitive
-- substring of the address or the display name; strpos, not LIKE, so a %
-- or _ in the search is matched as itself. The membership count reads
-- org_memberships across organizations, which this instance-level request
-- may (no organization is bound, so org_isolation_bypass applies).
SELECT
  u.id, u.email, u.display_name, u.instance_admin, u.disabled_at,
  (u.password_hash IS NOT NULL)::boolean AS has_password, u.must_change_password, u.last_sign_in_at,
  (EXISTS (SELECT 1 FROM totp_secrets t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
   OR EXISTS (SELECT 1 FROM passkeys p WHERE p.user_id = u.id))::boolean AS has_second_factor,
  (SELECT count(*) FROM org_memberships m WHERE m.user_id = u.id) AS org_count
FROM users u
WHERE (sqlc.arg(query)::text = ''
       OR strpos(lower(u.email::text), lower(sqlc.arg(query)::text)) > 0
       OR strpos(lower(u.display_name), lower(sqlc.arg(query)::text)) > 0)
  AND (sqlc.arg(after)::text = '' OR u.email > sqlc.arg(after)::text::citext)
ORDER BY u.email
LIMIT sqlc.arg(lim);

-- name: GetAccount :one
-- One account for the instance admin's account page: what ListAccounts
-- shows, plus the second factors and the sessions. A confirmed TOTP secret
-- is the authenticator app (an enrolment that was begun and never confirmed
-- is none); recovery codes count only while unspent; a session counts
-- while it is complete, unrevoked and unexpired — the ones GetSession would
-- still resolve to a signed-in principal.
SELECT
  u.id, u.email, u.display_name, u.instance_admin, u.disabled_at, u.created_at,
  (u.password_hash IS NOT NULL)::boolean AS has_password, u.must_change_password, u.temp_password_expires_at, u.last_sign_in_at,
  EXISTS (SELECT 1 FROM totp_secrets t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL) AS totp,
  (SELECT count(*) FROM passkeys p WHERE p.user_id = u.id) AS passkeys,
  (SELECT count(*) FROM recovery_codes r WHERE r.user_id = u.id AND r.used_at IS NULL) AS recovery_codes_left,
  (SELECT count(*) FROM sessions s WHERE s.user_id = u.id AND s.mfa_complete AND s.revoked_at IS NULL AND s.expires_at > now()) AS active_sessions,
  (SELECT count(*) FROM org_memberships m WHERE m.user_id = u.id) AS org_count
FROM users u
WHERE u.id = $1;

-- name: ListAccountMemberships :many
-- The organizations user_id belongs to, with their role, and whether
-- actor_id belongs to each as well (the account page links only those).
SELECT o.id AS org_id, o.slug, o.name, m.role::text AS role,
  EXISTS (SELECT 1 FROM org_memberships a WHERE a.org_id = m.org_id AND a.user_id = sqlc.arg(actor_id)) AS actor_is_member
FROM org_memberships m JOIN organizations o ON o.id = m.org_id
WHERE m.user_id = sqlc.arg(user_id)
ORDER BY o.name, o.id;

-- name: LockAccount :one
-- Whether the account has a password, whether it is deactivated and
-- whether it must change its password, locking its row for the rest of
-- the transaction, so an administrator's action and a concurrent change of
-- the same account (the owner's own password change, an invitation being
-- accepted, a reset) happen one after the other. A deactivated account is
-- found too: an administrator may act on it, and reactivating needs it.
-- has_second_factor is ListAccounts' expression (a confirmed TOTP secret or
-- a passkey): the forgot-password path issues no link without one
-- (backlog 7.102).
SELECT (u.password_hash IS NOT NULL)::boolean AS has_password, (u.disabled_at IS NOT NULL)::boolean AS deactivated,
  u.must_change_password,
  (EXISTS (SELECT 1 FROM totp_secrets t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
   OR EXISTS (SELECT 1 FROM passkeys p WHERE p.user_id = u.id))::boolean AS has_second_factor
FROM users u
WHERE u.id = $1
FOR UPDATE OF u;

-- name: SetTemporaryPassword :execrows
-- An administrator's temporary password: it forces a change at the next
-- sign-in and stops working at expires_at.
UPDATE users SET
  password_hash = sqlc.arg(password_hash)::text,
  must_change_password = true,
  temp_password_expires_at = sqlc.arg(expires_at)::timestamptz
WHERE id = sqlc.arg(id);

-- name: DeleteSecondFactors :exec
-- Removes every second factor of the account at once: the TOTP secret
-- (and with it the factor-attempt lockout), the recovery codes and every
-- passkey. One statement, so no state with only some of them gone exists.
WITH totp AS (
  DELETE FROM totp_secrets WHERE totp_secrets.user_id = $1
), codes AS (
  DELETE FROM recovery_codes WHERE recovery_codes.user_id = $1
)
DELETE FROM passkeys WHERE passkeys.user_id = $1;

-- name: ReactivateUser :execrows
-- Clears the deactivation. Revoked sessions and tokens stay revoked; the
-- service also revokes any session that is not (accounts.Reactivate).
UPDATE users SET disabled_at = NULL WHERE id = $1;

-- name: GetNoticeRecipient :one
-- Where an account notice goes, and in which language: the account's own
-- choice and the instance default (the default_locale instance setting, a
-- JSON string, read as text; '' when unset). The service resolves the two
-- the way /auth/me does (locale.Resolve).
SELECT u.email, u.locale,
  coalesce((SELECT s.value #>> '{}' FROM instance_settings s WHERE s.key = 'default_locale'), '')::text AS default_locale
FROM users u
WHERE u.id = $1;

-- name: GetAccountIDByEmail :one
-- The account with this address, for a forgotten password. email is
-- citext, so the match is case-insensitive.
SELECT id FROM users WHERE email = sqlc.arg(email)::text::citext;

-- name: CountResetTokensSince :one
-- How many reset tokens the account was issued since the given instant,
-- used or not: the per-address limit of the forgot route.
SELECT count(*) FROM password_reset_tokens
WHERE user_id = sqlc.arg(user_id) AND created_at >= sqlc.arg(since)::timestamptz;

-- name: VoidResetTokens :exec
-- Uses up every unused reset token of the account: a newer link voids the
-- older ones, and a completed reset voids all of them.
UPDATE password_reset_tokens SET used_at = sqlc.arg(at)::timestamptz
WHERE user_id = sqlc.arg(user_id) AND used_at IS NULL;

-- name: InsertResetToken :exec
-- Only the token's hash is stored; the token itself is in the mail.
INSERT INTO password_reset_tokens (id, user_id, token_hash, created_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(token_hash), sqlc.arg(created_at), sqlc.arg(expires_at));

-- name: ResetTokenOwner :one
-- The account a reset token belongs to, used or not, unlocked: a reset
-- locks the account first and the token second (LockResetToken), the order
-- every reset path takes, so it reads the owner before taking any lock.
SELECT user_id FROM password_reset_tokens WHERE token_hash = sqlc.arg(token_hash);

-- name: LockResetToken :one
-- The token row, locked for the rest of the transaction, while it belongs
-- to user_id and is unused and unexpired. The caller holds the account's
-- row lock already. Two resets with the same token serialize here, and the
-- second, re-checking used_at on the row the first committed, finds none.
SELECT id FROM password_reset_tokens
WHERE token_hash = sqlc.arg(token_hash) AND user_id = sqlc.arg(user_id)
  AND used_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz
FOR UPDATE;
