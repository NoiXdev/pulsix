-- +goose Up
-- Session lock with a PIN (backlog 3.93, spec docs/superpowers/specs/
-- 2026-11-17-session-lock-expiry-version-design.md). The PIN hash lives on the
-- account; prompted_at records that the one-time "set a PIN?" offer was shown,
-- so removing a PIN does not bring the offer back. A session is locked while
-- locked_at is set; unlock_failures counts wrong PINs since it was locked.
ALTER TABLE users ADD COLUMN lock_pin_hash text;
ALTER TABLE users ADD COLUMN lock_pin_prompted_at timestamptz;
ALTER TABLE sessions ADD COLUMN locked_at timestamptz;
ALTER TABLE sessions ADD COLUMN unlock_failures integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN unlock_failures;
ALTER TABLE sessions DROP COLUMN locked_at;
ALTER TABLE users DROP COLUMN lock_pin_prompted_at;
ALTER TABLE users DROP COLUMN lock_pin_hash;
