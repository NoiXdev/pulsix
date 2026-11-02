-- +goose Up
-- Backlog 1.41. A user's own GUI language. NULL means "no choice made": the
-- instance default language applies, and English when that is unset too.
-- The CHECK mirrors internal/locale; adding a language is a new migration
-- that widens it.
ALTER TABLE users ADD COLUMN locale text CHECK (locale IN ('en', 'de'));

-- +goose Down
ALTER TABLE users DROP COLUMN locale;
