-- +goose Up
-- Backlog 7.26. Guessing a second-factor code was limited only per client
-- address, so someone holding the password could spread guesses over many
-- addresses and work through the six-digit space. Each account now carries
-- its own budget on its TOTP row: every check of the confirmed factor (a
-- code or a recovery code) counts first, the fifth and every later one
-- lock the account for a doubling time capped at an hour, and a success
-- resets both columns. The recovery codes live and die with this row.
ALTER TABLE totp_secrets ADD COLUMN failed_factor_attempts integer NOT NULL DEFAULT 0;
ALTER TABLE totp_secrets ADD COLUMN factor_locked_until timestamptz;

-- +goose Down
ALTER TABLE totp_secrets DROP COLUMN factor_locked_until;
ALTER TABLE totp_secrets DROP COLUMN failed_factor_attempts;
