-- +goose Up
CREATE TABLE totp_secrets (
  user_id        uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_sealed  bytea NOT NULL,
  confirmed_at   timestamptz,
  last_used_step bigint NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE recovery_codes (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  bytea NOT NULL,
  used_at    timestamptz,
  PRIMARY KEY (user_id, code_hash)
);
-- +goose Down
DROP TABLE recovery_codes;
DROP TABLE totp_secrets;
