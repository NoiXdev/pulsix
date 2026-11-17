-- +goose Up
-- An organization admin's block of a personal token grant is per person and
-- organization (backlog 3.100, Task 17 fix rounds): every use of any of the
-- person's tokens in the organization, and every grant the person adds
-- there, asks whether they have a blocked grant in it. This partial index
-- answers that with one lookup; blocked rows are few.
CREATE INDEX personal_token_grants_blocked_idx ON personal_token_grants (org_id, user_id) WHERE blocked_at IS NOT NULL;

-- +goose Down
DROP INDEX personal_token_grants_blocked_idx;
