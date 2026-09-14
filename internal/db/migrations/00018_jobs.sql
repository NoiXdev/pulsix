-- +goose Up
-- The job queue (spec §7, backlog 2.11): git repositories, playbooks, job
-- templates and the jobs themselves, plus the job_events hypertable the
-- run view streams from. One migration because jobs references all three
-- parent tables.
--
-- worker_id on jobs references workers(id) with no foreign key constraint
-- on purpose: a worker row is revocable without losing the history of the
-- jobs it ran, so the reference is informational only.
--
-- job_events carries org_id but deliberately has NO row-level security:
-- the worker event path (gRPC, org-unscoped by design) appends without an
-- org scope, so a policy pair would force every append through an explicit
-- scope for no gain. The table is read only through jobs.Service.Events,
-- which resolves the job's organization first and then filters by org_id
-- in the WHERE clause — that check is unit-tested.
--
-- "limit" is quoted because LIMIT is a reserved key word in PostgreSQL's
-- grammar; trigger is not quoted (only TRIGGERING is reserved).

CREATE TABLE git_repos (
  id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name text NOT NULL, url text NOT NULL, branch text NOT NULL DEFAULT 'main',
  subpath text NOT NULL DEFAULT '', credential_id uuid,
  last_sync timestamptz, last_sync_error text,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id)
);

CREATE TABLE playbooks (
  id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  repo_id uuid, path text, inline text, source text NOT NULL CHECK (source IN ('repo','editor')),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, id),
  CHECK ((source = 'repo') = (repo_id IS NOT NULL AND path IS NOT NULL AND inline IS NULL)),
  CHECK ((source = 'editor') = (repo_id IS NULL AND path IS NULL AND inline IS NOT NULL)),
  FOREIGN KEY (org_id, repo_id) REFERENCES git_repos (org_id, id)
);

CREATE TABLE job_templates (
  id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name text NOT NULL, playbook_id uuid NOT NULL, scope jsonb NOT NULL,
  "limit" text NOT NULL DEFAULT '', extra_vars jsonb NOT NULL DEFAULT '{}',
  extra_vars_schema jsonb, credential_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name), UNIQUE (org_id, id),
  FOREIGN KEY (org_id, playbook_id) REFERENCES playbooks (org_id, id),
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id)
);

CREATE TABLE jobs (
  id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  zone_id uuid NOT NULL, kind text NOT NULL,
  template_id uuid, host_ids uuid[] NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','claimed','running','cancelling','succeeded','failed','cancelled','error')),
  worker_id uuid, claimed_at timestamptz, started_at timestamptz, finished_at timestamptz,
  stats jsonb NOT NULL DEFAULT '{}', error text NOT NULL DEFAULT '',
  run_group_id uuid NOT NULL, trigger text NOT NULL DEFAULT 'manual', actor_id uuid,
  payload jsonb, created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, id)
);
CREATE INDEX jobs_claim_idx ON jobs (status, zone_id, created_at) WHERE status = 'queued';

CREATE TABLE job_events (
  job_id uuid NOT NULL, seq bigint NOT NULL, org_id uuid NOT NULL,
  at timestamptz NOT NULL, host text NOT NULL DEFAULT '', task text NOT NULL DEFAULT '',
  state text NOT NULL, payload jsonb NOT NULL DEFAULT '{}',
  -- The primary key includes `at` because TimescaleDB requires every unique
  -- index on a hypertable to contain the partitioning column; a plain
  -- (job_id, seq) key cannot be created. Duplicate-sequence detection is
  -- therefore best-effort (the same seq with a different timestamp would
  -- insert), not a database guarantee — the append path is the single
  -- writer and sends strictly increasing seqs.
  PRIMARY KEY (job_id, seq, at)
);
SELECT create_hypertable('job_events', 'at', chunk_time_interval => INTERVAL '1 day');
ALTER TABLE job_events SET (timescaledb.compress);
SELECT add_compression_policy('job_events', INTERVAL '7 days');

-- Row-level security for the four org-owned tables, in the plan's final
-- policy-pair form: org_isolation admits only the bound organization,
-- org_isolation_bypass applies when no organization is bound (instance-
-- level statements and the org-unscoped claim path, which filters by
-- org_id in its own WHERE clauses).
ALTER TABLE git_repos ENABLE ROW LEVEL SECURITY;
ALTER TABLE git_repos FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON git_repos
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON git_repos
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE playbooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE playbooks FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON playbooks
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON playbooks
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE job_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON job_templates
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON job_templates
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON jobs
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON jobs
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE job_events;
DROP TABLE jobs;
DROP TABLE job_templates;
DROP TABLE playbooks;
DROP TABLE git_repos;
