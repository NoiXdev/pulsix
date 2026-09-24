-- +goose Up
-- A re-enrolment must not disarm the account (backlog 1.27). Asking for a
-- new TOTP secret used to overwrite the confirmed one and clear its
-- confirmation, so a user who opened the dialog and walked away was left
-- signing in with a password alone — with nothing on screen saying so.
--
-- The enrolling secret now lives beside the confirmed one until its first
-- code proves it. Both columns are null whenever no enrolment is in
-- flight, which is the normal state of every account.
ALTER TABLE totp_secrets ADD COLUMN pending_secret_sealed bytea;
ALTER TABLE totp_secrets ADD COLUMN pending_created_at timestamptz;

-- +goose Down
ALTER TABLE totp_secrets DROP COLUMN pending_created_at;
ALTER TABLE totp_secrets DROP COLUMN pending_secret_sealed;
