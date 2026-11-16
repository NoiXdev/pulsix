-- +goose Up
-- Job chaining (backlog 2.121, spec docs/superpowers/specs/2026-11-16-job-
-- chaining-design.md): a template names follow-up templates that start after
-- one of its runs succeeds. job_template_follow_ups holds the configuration;
-- run_follow_ups snapshots it per run group at launch and records what became
-- of each follow-up, so editing a template never changes a run in flight.

-- The follow-ups configured on a template. The owning side cascades with the
-- template; the follow-up side has no ON DELETE action (NO ACTION, checked
-- immediately, so it refuses exactly like RESTRICT), so deleting a template
-- that another one names as its follow-up fails instead of silently breaking
-- the chain. NO ACTION rather than RESTRICT because it reports SQLSTATE 23503,
-- which db.IsForeignKeyViolation recognises; RESTRICT reports 23001.
CREATE TABLE job_template_follow_ups (
  org_id                uuid NOT NULL,
  template_id           uuid NOT NULL,
  follow_up_template_id uuid NOT NULL,
  condition             text NOT NULL DEFAULT 'success' CHECK (condition IN ('success')),
  position              int  NOT NULL DEFAULT 0,
  PRIMARY KEY (template_id, follow_up_template_id),
  CHECK (template_id <> follow_up_template_id),
  FOREIGN KEY (org_id, template_id)           REFERENCES job_templates (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, follow_up_template_id) REFERENCES job_templates (org_id, id)
);

-- The follow-ups of one run group and what became of each. template_id has no
-- foreign key on purpose: the row outlives the template, like jobs.template_id.
CREATE TABLE run_follow_ups (
  id              uuid PRIMARY KEY,
  org_id          uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  parent_group_id uuid NOT NULL,
  template_id     uuid NOT NULL,
  condition       text NOT NULL DEFAULT 'success' CHECK (condition IN ('success')),
  position        int  NOT NULL DEFAULT 0,
  state           text NOT NULL DEFAULT 'pending'
                  CHECK (state IN ('pending', 'due', 'started', 'skipped', 'dismissed')),
  reason          text NOT NULL DEFAULT '',
  child_group_id  uuid,
  host_ids        uuid[] NOT NULL DEFAULT '{}',
  root_trigger    text NOT NULL CHECK (root_trigger IN ('manual', 'schedule', 'webhook', 'system', 'chain')),
  root_ref        uuid,
  depth           int  NOT NULL DEFAULT 1,
  path            uuid[] NOT NULL,
  configured_by   uuid,
  attempts        int  NOT NULL DEFAULT 0,
  next_attempt_at timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  decided_at      timestamptz,
  started_at      timestamptz
);

CREATE UNIQUE INDEX run_follow_ups_parent_template_idx ON run_follow_ups (parent_group_id, template_id);
CREATE INDEX run_follow_ups_due_idx ON run_follow_ups (next_attempt_at) WHERE state = 'due';
CREATE INDEX run_follow_ups_pending_idx ON run_follow_ups (created_at) WHERE state = 'pending';
CREATE INDEX run_follow_ups_child_idx ON run_follow_ups (child_group_id);

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['job_template_follow_ups', 'run_follow_ups'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY org_isolation ON %I USING (org_id = NULLIF(current_setting(''pulsix.org_id'', true), '''')::uuid)', t);
    EXECUTE format('CREATE POLICY org_isolation_bypass ON %I USING (current_setting(''pulsix.org_id'', true) IS NULL OR current_setting(''pulsix.org_id'', true) = '''')', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- A chained run carries trigger = 'chain' and trigger_ref = the parent run
-- group. Widening the CHECK is additive: every existing row still passes.
ALTER TABLE jobs DROP CONSTRAINT jobs_trigger_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_trigger_check
  CHECK (trigger IN ('manual', 'schedule', 'webhook', 'system', 'chain'));

-- +goose Down
ALTER TABLE jobs DROP CONSTRAINT jobs_trigger_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_trigger_check
  CHECK (trigger IN ('manual', 'schedule', 'webhook', 'system'));
DROP TABLE run_follow_ups;
DROP TABLE job_template_follow_ups;
