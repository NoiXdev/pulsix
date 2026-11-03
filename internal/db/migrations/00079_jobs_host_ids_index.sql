-- +goose Up
-- The host view's run list (ListJobs with host_id) asks "which runs
-- targeted this host", which is an array containment test on
-- jobs.host_ids. Without an index that is a scan of the organization's whole
-- run history; a GIN index on the array answers it directly. A host id is a
-- uuid unique across organizations, so the index carries host_ids alone
-- (a composite with org_id would need the btree_gin extension for nothing).
CREATE INDEX jobs_host_ids_idx ON jobs USING gin (host_ids);

-- +goose Down
DROP INDEX jobs_host_ids_idx;
