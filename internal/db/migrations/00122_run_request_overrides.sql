-- +goose Up
-- What a run request changes about its template's run (spec 2026-11-25
-- assistant API gaps, A): a dry run (check, diff), extra vars replacing the
-- template's top-level ones, the hosts the run is narrowed to and a limit
-- narrowing the template's own, as a JSON object
-- (runrequests.Overrides). Set when the request is made, never changed
-- after. A request cannot set a secret extra var, so the document holds
-- none and is not sealed. NULL is a request without overrides: every row
-- from before this column, so it needs no backfill and the ADD COLUMN
-- touches only the catalog.
ALTER TABLE run_requests ADD COLUMN overrides jsonb;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE run_requests DROP COLUMN overrides;
