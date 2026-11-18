-- +goose Up
-- The lock PIN is exactly four digits from this release on. The length of the
-- PIN is recorded when it is set; a PIN set under the previous release (4 to 8
-- digits, length not stored) keeps NULL here, which marks it as an older PIN
-- that still unlocks but that the person is asked to replace.
ALTER TABLE users ADD COLUMN lock_pin_digits smallint CHECK (lock_pin_digits BETWEEN 4 AND 8);

-- +goose Down
ALTER TABLE users DROP COLUMN lock_pin_digits;
