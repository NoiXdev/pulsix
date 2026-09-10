-- +goose Up
CREATE TABLE sessions (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  secret_hash  bytea NOT NULL,
  mfa_complete boolean NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  revoked_at   timestamptz
);
CREATE INDEX sessions_user_idx ON sessions(user_id);
-- +goose Down
DROP TABLE sessions;
