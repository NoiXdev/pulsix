-- +goose Up
-- When the session last proved a second factor (backlog 1.28). The
-- account-security actions — registering or deleting a passkey, moving or
-- disabling TOTP — require a recently proven factor, so that a session
-- left open on an unlocked screen cannot be used to quietly take the
-- account over by adding a credential to it.
--
-- Nullable: every session that exists today proved its factor at a time
-- nobody recorded, and inventing one would be a lie in either direction.
-- A session with no timestamp is treated as not fresh — it re-proves once
-- and carries a real one from then on.
ALTER TABLE sessions ADD COLUMN mfa_at timestamptz;

-- +goose Down
ALTER TABLE sessions DROP COLUMN mfa_at;
