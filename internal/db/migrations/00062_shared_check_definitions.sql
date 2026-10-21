-- +goose Up
-- shared_check_definitions is a check definition the INSTALLATION owns,
-- projected into every organization as an ordinary check_definitions row —
-- the third and last application of the mechanism migration 00060 built for
-- repositories and 00061 extended to job templates (spec v0.2f decisions 1,
-- 2). No org_id, no RLS, no policy pair: the same instance_settings posture
-- shared_repos and shared_job_templates already carry.
--
-- key, plugin, params and interval_seconds are the four columns spec
-- decision 2's table gives the definition; binding_kind, group_id, host_id
-- and enabled stay exactly where they already are, on check_definitions
-- itself — a group or host is the customer's own data the installation has
-- no way to know, so the binding is something the customer fills in, like a
-- credential, never something this migration resolves automatically (see
-- internal/checks/definitions.go's own comment on the projection's half,
-- and task-7-brief's own reasoning for why no organization-side resolution
-- trigger is needed here the way migration 00061 needed one for
-- job_templates.playbook_id).
--
-- No separate `name` column, unlike shared_repos/shared_job_templates:
-- check_definitions never had one either. `key` has always been the
-- identifier a narrower scope overrides on (migration 00043's own comment)
-- and is what decision 2's table lists for a check definition from its
-- first draft — unlike repositories and job templates, where `name` was a
-- gap the design doc's own text records being found later, a check
-- definition's "name" was never missing because `key` already played that
-- role.
CREATE TABLE shared_check_definitions (
  id               uuid PRIMARY KEY,
  key              text NOT NULL UNIQUE,
  plugin           text NOT NULL,
  params           jsonb NOT NULL DEFAULT '{}'::jsonb,
  interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 10 AND 86400),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);

-- shared_definition_id non-null means this check_definitions row is a
-- projection — git_repos.shared_repo_id's own comment (migration 00060)
-- applies unchanged: single-column because the parent has no org_id to
-- compose with. ON DELETE RESTRICT matches the other two kinds for the same
-- structural reason (spec decision 5 is reported as ErrInUse before the
-- database ever has to refuse), even though nothing in this schema
-- currently references check_definitions at all — grepped every migration
-- for "REFERENCES check_definitions" and found no hits, confirmed again
-- here by re-deriving it from the live catalog, not merely trusting the
-- grep: check_samples and check_current are deliberately keyed by (org_id,
-- host_id, check_key), not by check_definitions.id, exactly so editing or
-- deleting a definition never orphans or rewrites the history an operator
-- is looking at on a graph (migration 00043's own Definition doc comment).
-- So internal/shared's DeleteWithProjections for this kind has no dependant
-- table to check before removing a projection — a legitimate, simpler case
-- than repositories (job triggers, playbooks) and job templates (schedules,
-- job triggers), not an oversight; it would stop being true only if a
-- future table started referencing check_definitions.id directly, which
-- would need its own migration and its own explicit check in
-- DeleteCheckDefinitionWithProjections, the same way 00060/00061 added
-- theirs.
ALTER TABLE check_definitions
  ADD COLUMN shared_definition_id uuid REFERENCES shared_check_definitions (id) ON DELETE RESTRICT;

-- origin gains a third value for a projected row. Replacing a CHECK
-- constraint is additive (DROP + ADD costs nothing to any existing row,
-- none of which can already be 'shared' before this migration runs);
-- dropping the column would not be, which is why this is a constraint
-- replacement rather than a new, separate boolean "is_shared" column next
-- to the existing one.
ALTER TABLE check_definitions DROP CONSTRAINT check_definitions_origin_check;
ALTER TABLE check_definitions ADD CONSTRAINT check_definitions_origin_check
  CHECK (origin IN ('seed', 'custom', 'shared'));

-- +goose Down
-- NOT VALID: a row stamped 'shared' by this migration's own feature stays
-- exactly as it is; the narrower CHECK applies to new rows only, so the
-- rollback never fails and deletes nothing (00032_maintenance_windows.sql's
-- own Down does the same for schedule_runs.outcome).
ALTER TABLE check_definitions DROP CONSTRAINT check_definitions_origin_check;
ALTER TABLE check_definitions ADD CONSTRAINT check_definitions_origin_check
  CHECK (origin IN ('seed', 'custom')) NOT VALID;
ALTER TABLE check_definitions DROP COLUMN shared_definition_id;
DROP TABLE shared_check_definitions;
