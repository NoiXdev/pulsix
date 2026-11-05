-- +goose Up
-- The runs list of one host (backlog 7.84). 00079 gave jobs.host_ids a GIN
-- index for ListJobs' "host_ids @> ARRAY[$host]", but that index never
-- served the manager: under row-level security the planner only moves a
-- condition into an index scan ahead of the policy's own when its operator
-- is leakproof, and array containment is not. job_hosts links each job to
-- the hosts it targeted, and uuid equality on host_id is leakproof.
--
-- created_at is the job's own (immutable) creation time, copied so that the
-- list's order and keyset cursor come from job_hosts_host_idx: a host's
-- list reads its newest links instead of sorting every run it ever had.
-- The primary key serves the cascade from jobs, the host index the cascade
-- from hosts. Both foreign keys are composite on (org_id, ...), so a link
-- never pairs a job of one organization with a host of another.
CREATE TABLE job_hosts (
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  job_id     uuid NOT NULL,
  host_id    uuid NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (job_id, host_id),
  FOREIGN KEY (org_id, job_id) REFERENCES jobs (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);

ALTER TABLE job_hosts ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_hosts FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON job_hosts
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON job_hosts
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- The run history, linked once: one row per distinct (job, host) pair
-- whose host still exists in the job's organization. Not batched: goose
-- runs this file in one transaction, so batches inside it buy nothing.
-- The migration runs with no organization bound, so the bypass policy lets
-- it see every organization. The index is built after the fill.
INSERT INTO job_hosts (org_id, job_id, host_id, created_at)
SELECT DISTINCT j.org_id, j.id, h.id, j.created_at
FROM jobs j
CROSS JOIN LATERAL unnest(j.host_ids) AS t(host_id)
JOIN hosts h ON h.org_id = j.org_id AND h.id = t.host_id;

CREATE INDEX job_hosts_host_idx ON job_hosts (org_id, host_id, created_at DESC, job_id DESC);

-- Nothing reads jobs.host_ids with @> any more (ListHostsDueForTest uses
-- = ANY, which a GIN index cannot serve), and the index cost every job
-- insert a GIN update. An older manager refuses this schema
-- (CheckSchemaNotNewer), so nothing needs the index after this migration;
-- docs/upgrades.md says to stop every manager first.
DROP INDEX jobs_host_ids_idx;

-- +goose Down
CREATE INDEX jobs_host_ids_idx ON jobs USING gin (host_ids);
DROP TABLE job_hosts;
