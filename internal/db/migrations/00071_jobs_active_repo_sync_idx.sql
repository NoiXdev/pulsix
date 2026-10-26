-- +goose Up
-- Backlog 2.68 (v0.2c final review, T4). HasActiveRepoSync is the
-- repository half of the webhook trigger's on_active=skip guard, and it
-- runs on every delivery. Its template sibling has had
-- jobs_active_template_idx since migration 00024; the repository one had
-- nothing, so each check was a scan of jobs evaluating payload->>'repo_id'
-- on every row it read — a cost growing with the whole run history rather
-- than with the number of syncs actually in flight, which is all the
-- question is about.
--
-- Being PARTIAL is where almost all of the win is: a finished job leaves
-- the index, so it only ever holds the syncs currently running. That also
-- makes it immune to the payload wipe a terminal transition performs —
-- every status that wipes the payload is a status this predicate excludes.
--
-- The payload expression is the second column and, under row-level
-- security, it does not become an Index Cond: `->>` is not leakproof, so
-- Postgres may not evaluate it before the policy's own predicate and
-- leaves it as a Filter over the rows the index already returned. That is
-- measured, not assumed (see TestActiveRepoSyncIndexServesItsQuery) and it
-- is fine — those rows are just the organization's in-flight syncs. The
-- column stays because it is nearly free on a set that small and it is
-- what the query would use if that restriction ever lifts; do not "fix"
-- this by putting the expression first, which would only move the problem
-- to org_id.
CREATE INDEX jobs_active_repo_sync_idx ON jobs (org_id, (payload ->> 'repo_id'))
  WHERE kind = 'sync_repo' AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error');

-- +goose Down
DROP INDEX jobs_active_repo_sync_idx;
