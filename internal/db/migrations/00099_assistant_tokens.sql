-- +goose Up
-- Assistant tokens (spec docs/superpowers/specs/2026-11-16-assistant-access-
-- design.md, "Assistant tokens"). An api token is the existing kind; an
-- assistant token works only at the assistant endpoint and carries the
-- areas its owner chose (tenancy.Area values). The CHECK keeps the two
-- shapes apart: an assistant token always has at least one area, an api
-- token never has any. Existing rows default to api.
ALTER TABLE api_tokens
  ADD COLUMN kind text NOT NULL DEFAULT 'api' CHECK (kind IN ('api', 'assistant')),
  ADD COLUMN areas text[],
  ADD CONSTRAINT api_tokens_kind_areas CHECK (
    (kind = 'api' AND areas IS NULL) OR
    (kind = 'assistant' AND areas IS NOT NULL AND cardinality(areas) > 0)
  );

-- +goose Down
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_kind_areas, DROP COLUMN areas, DROP COLUMN kind;
