-- +goose Up
-- Agent enrollment inside an approved run (spec 2026-11-25 assistant API
-- gaps, B, decision D2). A job template with agent_enrollment on enrols
-- the Pulsix agent on the hosts it runs on: its runs hand each target
-- without an agent a single-use enrollment token. Off for every existing
-- template; a constant default, so the ADD COLUMN touches only the catalog.
ALTER TABLE job_templates ADD COLUMN agent_enrollment boolean NOT NULL DEFAULT false;

-- The hosts an approval enrols agents on: the picture's targets without an
-- agent, stored with the decision on the request and on every job it
-- enqueues, so the claim compares stored values instead of recomputing
-- them (like pinned_commit, migration 00123). An empty array is an
-- approved list with no host on it (every target had an agent, or a dry
-- run); NULL is no list: a template without the flag, a request not
-- approved or approved before this column, and every job not started by an
-- approval. No foreign key, like host_ids: the history outlives a deleted
-- host. Nullable without a default, so both touch only the catalog.
ALTER TABLE run_requests ADD COLUMN enroll_host_ids uuid[];
ALTER TABLE jobs ADD COLUMN enroll_host_ids uuid[];

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE jobs DROP COLUMN enroll_host_ids;
ALTER TABLE run_requests DROP COLUMN enroll_host_ids;
ALTER TABLE job_templates DROP COLUMN agent_enrollment;
