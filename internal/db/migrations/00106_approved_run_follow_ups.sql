-- +goose Up
-- The per-organization choice whether an approved run request also starts the
-- template's follow-ups (spec docs/superpowers/specs/2026-11-16-approved-run-
-- follow-ups-design.md, "Data model"). Off by default, so approving stays what
-- it was; an absent row keeps meaning off.
ALTER TABLE org_assistant_settings ADD COLUMN approved_runs_start_follow_ups boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE org_assistant_settings DROP COLUMN approved_runs_start_follow_ups;
