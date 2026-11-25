-- +goose Up
-- Requests of more kinds (spec 2026-11-25 assistant API gaps, C, decision
-- D1): beside a run (run) and a retry (retry_failed), an assistant asks for
-- a schedule to be created, changed or resumed (schedule) and, in later
-- steps, for a webhook trigger (trigger) and for a person to review a
-- schedule or trigger an assistant's change paused (schedule_review,
-- trigger_review). One table, one approvals page: the kind says what the
-- request does, the lifecycle (pending, approved, rejected, expiry, the
-- per-token cap) is the same for all. The kind CHECK only widens: every
-- existing row passes it.
ALTER TABLE run_requests DROP CONSTRAINT run_requests_kind_check;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_kind_check
  CHECK (kind IN ('run', 'retry_failed', 'schedule', 'trigger', 'schedule_review', 'trigger_review'));

-- payload is what a request of a kind other than run and retry_failed
-- would create or change (for a schedule: the action and the schedule as
-- it will be), never a secret: a kept secret is a marker naming the stored
-- one. NULL for runs and retries, whose overrides have their own column.
-- target_id is the object the request acts on (for a schedule: the one it
-- creates, its id taken when the request is made, or the one it changes or
-- resumes). No foreign key, like job_id: the history outlives the object.
-- Both nullable without a default, so they touch only the catalog.
ALTER TABLE run_requests ADD COLUMN payload jsonb;
ALTER TABLE run_requests ADD COLUMN target_id uuid;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_payload_check
  CHECK ((kind IN ('run', 'retry_failed')) = (payload IS NULL));

-- A webhook trigger that syncs a repository runs no template, so its
-- request (and a review of it) has none: template_id becomes nullable for
-- those two kinds only, and every other kind still names its template.
-- Dropping NOT NULL and adding a CHECK every existing row passes touch no
-- row but the check's scan.
ALTER TABLE run_requests ALTER COLUMN template_id DROP NOT NULL;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_template_check
  CHECK (template_id IS NOT NULL OR kind IN ('trigger', 'trigger_review'));

-- The parts of a schedule's overrides an assistant set (an approved schedule
-- request): the limit and the extra vars carry their own mark, so an
-- assistant's limit leaves a person's extra vars as they are and the other
-- way round. A marked part reaches the schedule's runs as untrusted values,
-- which the claim re-checks and marks unsafe for Ansible, as a run
-- request's. False for every existing schedule; constant defaults touch
-- only the catalog..
ALTER TABLE schedules ADD COLUMN untrusted_limit boolean NOT NULL DEFAULT false;
ALTER TABLE schedules ADD COLUMN untrusted_extra_vars boolean NOT NULL DEFAULT false;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE schedules DROP COLUMN untrusted_extra_vars;
ALTER TABLE schedules DROP COLUMN untrusted_limit;
DELETE FROM run_requests WHERE kind NOT IN ('run', 'retry_failed');
ALTER TABLE run_requests DROP CONSTRAINT run_requests_template_check;
ALTER TABLE run_requests ALTER COLUMN template_id SET NOT NULL;
ALTER TABLE run_requests DROP CONSTRAINT run_requests_payload_check;
ALTER TABLE run_requests DROP COLUMN target_id;
ALTER TABLE run_requests DROP COLUMN payload;
ALTER TABLE run_requests DROP CONSTRAINT run_requests_kind_check;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_kind_check
  CHECK (kind IN ('run', 'retry_failed'));
