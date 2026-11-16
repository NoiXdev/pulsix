-- +goose Up
-- Audit entries name the API token a change was made with (spec
-- docs/superpowers/specs/2026-11-16-assistant-access-design.md, "Audit").
-- Nullable: sessions and system events have none, nor does every event
-- recorded before this column. No foreign key, so the history outlives the
-- token's row should one ever be deleted (tokens are revoked, not deleted).
ALTER TABLE audit_events ADD COLUMN actor_token_id uuid;

-- +goose Down
ALTER TABLE audit_events DROP COLUMN actor_token_id;
