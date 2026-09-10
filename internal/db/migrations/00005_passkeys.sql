-- +goose Up
CREATE TABLE passkeys (
  id              uuid PRIMARY KEY,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name            text NOT NULL,
  credential_id   bytea NOT NULL UNIQUE,
  public_key      bytea NOT NULL,
  attestation     text NOT NULL,
  transports      text[] NOT NULL DEFAULT '{}',
  sign_count      bigint NOT NULL DEFAULT 0,
  backup_eligible boolean NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_used_at    timestamptz
);
CREATE INDEX passkeys_user_idx ON passkeys(user_id);
CREATE TABLE webauthn_ceremonies (
  id         uuid PRIMARY KEY,
  kind       text NOT NULL,
  user_id    uuid,
  data       bytea NOT NULL,
  expires_at timestamptz NOT NULL
);
-- +goose Down
DROP TABLE webauthn_ceremonies;
DROP TABLE passkeys;
