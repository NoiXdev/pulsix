-- +goose Up
-- Account administration (backlog 7.74). must_change_password is the
-- forced-change flag an administrator's set or temporary password raises and
-- the owner's own change clears; temp_password_expires_at is when such a
-- temporary password stops working; last_sign_in_at is the last time a
-- session became complete (password-only accounts at password check, others
-- once their second factor was proven). All three are additive and
-- nullable or defaulted, so a running older release is unaffected.
ALTER TABLE users
  ADD COLUMN must_change_password boolean NOT NULL DEFAULT false,
  ADD COLUMN temp_password_expires_at timestamptz,
  ADD COLUMN last_sign_in_at timestamptz;

-- Only the hash of a reset token is stored; the plain token travels in the
-- mail and nowhere else. Like users and sessions this table is
-- instance-level: a password reset belongs to the account, not to an
-- organization, so it carries no RLS.
CREATE TABLE password_reset_tokens (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  used_at    timestamptz
);

-- Serves "the newest tokens of this user", which the request throttle and
-- the invalidation of older tokens both ask.
CREATE INDEX password_reset_tokens_user_created_idx ON password_reset_tokens (user_id, created_at);

-- +goose Down
DROP INDEX password_reset_tokens_user_created_idx;
DROP TABLE password_reset_tokens;
ALTER TABLE users
  DROP COLUMN last_sign_in_at,
  DROP COLUMN temp_password_expires_at,
  DROP COLUMN must_change_password;
