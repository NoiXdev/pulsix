-- name: ListOrgsWithShorterRetention :many
-- The retention purge's work list (backlog 3.15, monitorsettings.Purger):
-- every organization whose retention choice is below the instance maximum
-- (max_days, PULSIX_SAMPLE_RETENTION): exactly the organizations whose
-- monitorsettings.EffectiveRetentionDays is below the maximum, with the
-- choice as that value (TestListOrgsMatchesTheEffectiveRetention pins the
-- two together; the purger applies EffectiveRetentionDays again itself).
-- NULL (no choice) and a choice at or above the maximum leave the
-- organization to TimescaleDB's retention policy, so they are not listed. Runs outside any organization scope (the bypass
-- policy of org_monitoring_settings applies), like the other cross-org
-- tickers' reads.
SELECT org_id, retention_days::integer AS retention_days
FROM org_monitoring_settings
WHERE retention_days < sqlc.arg(max_days)::integer
ORDER BY org_id;

-- name: DeleteOrgCheckSamplesBatch :execrows
-- Deletes at most row_limit of one organization's check_samples rows with
-- range_start <= at < range_end, one batch of the retention purge. The
-- caller passes one TimescaleDB chunk's range (clamped to the cutoff), so
-- every row the subquery returns lives in that one chunk and its ctid is
-- unique there: a ctid alone would be ambiguous across chunks. The outer
-- predicates repeat org and range, so even a range spanning several
-- chunks could only ever delete more of this organization's rows in the
-- range, never another organization's and never a newer one. Plan
-- (TimescaleDB 2.30, generic plan, 31 populated chunks): the subquery and
-- the delete each run a ChunkAppend with 30 of 31 chunks excluded at
-- startup, then an index scan of the one chunk's check_samples_lookup_idx
-- (org_id, ..., at) with org_id and the range as index conditions; with
-- many rows of the org in the chunk the delete becomes a Tid Scan of the
-- collected ctids instead. An empty range costs about half a millisecond.
-- Runs inside the organization's scope (row-level security as a second
-- fence).
DELETE FROM check_samples d
WHERE d.org_id = sqlc.arg(org_id)
  AND d.at >= sqlc.arg(range_start) AND d.at < sqlc.arg(range_end)
  AND d.ctid = ANY(ARRAY(
    SELECT s.ctid FROM check_samples s
    WHERE s.org_id = sqlc.arg(org_id)
      AND s.at >= sqlc.arg(range_start) AND s.at < sqlc.arg(range_end)
    LIMIT sqlc.arg(row_limit)::integer
  ));

-- name: DeleteOrgCheckStateChangesBatch :execrows
-- DeleteOrgCheckSamplesBatch for check_state_changes (same reasoning; the
-- index is check_state_changes_lookup_idx).
DELETE FROM check_state_changes d
WHERE d.org_id = sqlc.arg(org_id)
  AND d.at >= sqlc.arg(range_start) AND d.at < sqlc.arg(range_end)
  AND d.ctid = ANY(ARRAY(
    SELECT c.ctid FROM check_state_changes c
    WHERE c.org_id = sqlc.arg(org_id)
      AND c.at >= sqlc.arg(range_start) AND c.at < sqlc.arg(range_end)
    LIMIT sqlc.arg(row_limit)::integer
  ));
