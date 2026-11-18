-- +goose Up
-- A session opened complete on the password alone because the second factor
-- is optional (PULSIX_MFA_OPTIONAL) is marked here. Its completeness and its
-- step-up freshness hold only while the account has no second factor: the
-- session lookup (GetSession) reads such a session as pending once a TOTP
-- secret is confirmed or a passkey registered, until a real factor proof
-- clears the mark (MarkSessionMFAComplete). Every existing session was
-- completed by a factor, or is pending, so false is right for all of them.
ALTER TABLE sessions ADD COLUMN password_only boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE sessions DROP COLUMN password_only;
