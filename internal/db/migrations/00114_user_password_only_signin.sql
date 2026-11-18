-- +goose Up
-- When the account last opened a session on its password alone because the
-- second factor is optional (PULSIX_MFA_OPTIONAL). The account's first
-- second factor revokes every API token of the account while this is set,
-- whichever session enrols it and whether the mode is still on, and clears
-- it in the same transaction: tokens minted on the password alone must not
-- outlive the factor, even if the mode was turned off in between. NULL for
-- every existing account: none signed in that way under an earlier release.
ALTER TABLE users ADD COLUMN password_only_signin_at timestamptz;

-- +goose Down
ALTER TABLE users DROP COLUMN password_only_signin_at;
