-- Run requests (spec 2026-11-16 assistant access). Org-scoped like every
-- store query; each statement also names org_id.

-- name: InsertRunRequest :exec
-- A request is pending (expires_at set) or, made with sync_first, waiting
-- for its sync (expires_at NULL, sync_job_id set; migration 00124). A
-- request of another kind than run or retry_failed carries its payload and
-- target (migration 00129).
INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, host_id, job_id, reason,
  requested_by_user, requested_by_token, status, expires_at, created_at, overrides, sync_job_id, expires_in_minutes,
  payload, target_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18);

-- name: GetRunRequest :one
SELECT * FROM run_requests WHERE org_id = $1 AND id = $2;

-- name: ListRunRequests :many
-- Newest first, at most the 500 most recent: a list view, not an archive.
-- The status filter is the effective status (backlog 2.120), applied before
-- the cap so decided rows never hide a pending one: "expired" is a pending
-- row at or past expires_at on the caller's clock (now), "pending" one
-- before it. A NULL status lists every row.
SELECT * FROM run_requests
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(status)::text IS NULL OR sqlc.narg(status)::text = CASE
    WHEN status = 'pending' AND expires_at <= sqlc.arg(now)::timestamptz THEN 'expired' ELSE status END)
ORDER BY created_at DESC, id DESC LIMIT 500;

-- name: CountPendingRunRequests :one
-- The pending, unexpired requests of the organization (the approval badge)
-- or, when token is given, of that one token (the per-token cap), which
-- also counts the token's requests waiting for their sync (migration
-- 00124): they become pending without asking again.
-- A review (migration 00130) is not what the token asked for: it counts
-- on the badge, not against the token's cap.
SELECT count(*)::int AS n FROM run_requests
WHERE org_id = sqlc.arg(org_id)
  AND ((status = 'pending' AND expires_at > sqlc.arg(now)::timestamptz)
    OR (sqlc.narg(token)::uuid IS NOT NULL AND status = 'waiting_for_sync'))
  AND (sqlc.narg(token)::uuid IS NULL OR (requested_by_token = sqlc.narg(token)::uuid
    AND kind NOT IN ('schedule_review', 'trigger_review')));

-- name: ListRunRequestTokens :many
-- The tokens that made requests, by id (backlog 2.120): the name and whether
-- the token can still ask here. requested_by_token has no foreign key, so a
-- deleted token is simply missing. An organization token is confined to
-- the organization (row-level security and org_id); a personal token
-- (backlog 3.100) reads as revoked here when it is revoked, its grant for
-- this organization is gone or blocked (its owner's block included), or the
-- organization no longer allows personal tokens. A deactivated owner stops
-- both kinds. These are the sign-in rules of GetAPITokenByHash (tokens.sql),
-- GetPersonalTokenByHash, GetPersonalTokenGrant and PersonalTokensAllowed
-- (personal_tokens.sql): change them together. Columns are named, never
-- t.*: secret_hash is not read.
SELECT t.id, t.name, (t.revoked_at IS NOT NULL OR u.disabled_at IS NOT NULL)::boolean AS revoked, t.expires_at
FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.org_id = sqlc.arg(org_id)::uuid AND t.id = ANY(sqlc.arg(ids)::uuid[])
UNION ALL
SELECT t.id, t.name, (
  t.revoked_at IS NOT NULL OR u.disabled_at IS NOT NULL OR g.token_id IS NULL OR g.blocked_at IS NOT NULL
  OR EXISTS (SELECT 1 FROM personal_token_grants b
             WHERE b.org_id = sqlc.arg(org_id)::uuid AND b.user_id = t.user_id AND b.blocked_at IS NOT NULL)
  OR NOT COALESCE((SELECT s.personal_tokens_allowed FROM org_token_settings s WHERE s.org_id = sqlc.arg(org_id)::uuid), true)
)::boolean AS revoked, t.expires_at
FROM personal_tokens t JOIN users u ON u.id = t.user_id
LEFT JOIN personal_token_grants g ON g.token_id = t.id AND g.org_id = sqlc.arg(org_id)::uuid
WHERE t.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DecideRunRequest :execrows
-- Only a pending row moves, and a rejection also moves one waiting for its
-- sync (whose expiry, never started, becomes the decision's time);
-- zero rows means it was already decided. An approval records the
-- fingerprint the person approved and takes the request's own id as the
-- run group, before anything is enqueued, so a claim of one of its jobs
-- always finds it (migration 00103), and the commit the run is pinned to
-- (migration 00123; NULL for none) and the hosts it enrols agents on
-- (migration 00127; NULL for no list). Only a run or a retry takes a run
-- group: the approval of another kind starts no run (migration 00129).
UPDATE run_requests SET status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_at = sqlc.arg(decided_at),
  approved_fingerprint = sqlc.narg(approved_fingerprint), pinned_commit = sqlc.narg(pinned_commit),
  enroll_host_ids = sqlc.narg(enroll_host_ids)::uuid[],
  run_group_id = CASE WHEN sqlc.arg(status)::text = 'approved' AND kind IN ('run', 'retry_failed') THEN id ELSE run_group_id END,
  expires_at = COALESCE(expires_at, sqlc.arg(decided_at))
WHERE org_id = sqlc.arg(org_id)::uuid AND id = sqlc.arg(id)
  AND (status = 'pending' OR (status = 'waiting_for_sync' AND sqlc.arg(status)::text = 'rejected'));

-- name: RevertRunRequest :exec
-- A failed enqueue puts the request back to pending. run_group_id stays:
-- jobs the enqueue did create keep pointing at this request, and its
-- claim guard refuses them while the request is not approved.
UPDATE run_requests SET status = 'pending', decided_by = NULL, decided_at = NULL, approved_fingerprint = NULL, pinned_commit = NULL,
  enroll_host_ids = NULL
WHERE org_id = $1 AND id = $2;

-- name: GetRunRequestByRunGroup :one
SELECT * FROM run_requests WHERE org_id = $1 AND run_group_id = $2;

-- name: LockRunRequestStatusByRunGroup :one
-- The status of the request whose approval created a run group, read under
-- a FOR SHARE lock held to the end of the caller's transaction: an approval
-- (DecideRunRequest) of the same request waits for that transaction, and a
-- read that waited on an approval returns the approved row (backlog 2.127).
SELECT status FROM run_requests WHERE org_id = $1 AND run_group_id = $2 FOR SHARE;

-- name: ListWaitingRunRequestsForSync :many
-- The requests waiting for one sync job (migration 00124), with the job's
-- status and error.
SELECT r.org_id, r.id, r.created_at, r.expires_in_minutes, r.sync_job_id,
  j.status AS sync_status, j.error AS sync_error
FROM run_requests r
LEFT JOIN jobs j ON j.org_id = r.org_id AND j.id = r.sync_job_id
WHERE r.status = 'waiting_for_sync' AND r.sync_job_id = sqlc.arg(sync_job_id)::uuid
ORDER BY r.created_at, r.id;

-- name: ListDueWaitingRunRequests :many
-- The waiting requests the sweep moves: their sync job ended or is gone
-- (status and error NULL), or they have waited since before stale_before.
-- A request whose sync still runs in time is not listed, so however many
-- there are they never hide a due one. The partial index on the waiting
-- requests holds the candidates, the job is a primary key probe. The
-- manager's sweep runs without an organization scope and sees every
-- organization's.
SELECT r.org_id, r.id, r.created_at, r.expires_in_minutes, r.sync_job_id,
  j.status AS sync_status, j.error AS sync_error
FROM run_requests r
LEFT JOIN jobs j ON j.org_id = r.org_id AND j.id = r.sync_job_id
WHERE r.status = 'waiting_for_sync'
  AND (j.id IS NULL OR j.status IN ('succeeded', 'failed', 'cancelled', 'error')
    OR r.created_at <= sqlc.arg(stale_before)::timestamptz)
ORDER BY r.created_at, r.id;

-- name: EndRunRequestWait :execrows
-- A waiting request becomes pending (its expiry starts) or failed (the
-- expiry is the failure's time, sync_error the reason); zero rows means it
-- no longer waits.
UPDATE run_requests SET status = sqlc.arg(status), expires_at = sqlc.arg(expires_at), sync_error = sqlc.narg(sync_error)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND status = 'waiting_for_sync';

-- name: GetOpenReviewRequest :one
-- The open review of a paused schedule or trigger (spec 2026-11-25 C2,
-- migration 00130): pending and unexpired at now; the newest if there were
-- ever more.
SELECT * FROM run_requests
WHERE org_id = sqlc.arg(org_id) AND kind = sqlc.arg(kind) AND target_id = sqlc.arg(target_id)::uuid
  AND status = 'pending' AND expires_at > sqlc.arg(now)::timestamptz
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: RefreshReviewRequest :execrows
-- Another assistant change hit an item already paused for review: its
-- open review shows every change and its expiry starts again.
UPDATE run_requests SET payload = sqlc.arg(payload), expires_at = sqlc.arg(expires_at), template_revision = sqlc.arg(template_revision)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND status = 'pending';

-- name: CloseReviewRequests :execrows
-- A person resumed or enabled the item directly (approved: they looked at
-- it), or the item was deleted (rejected: nothing is left to resume): its
-- open reviews are decided by that person.
UPDATE run_requests SET status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_at = sqlc.arg(decided_at)
WHERE org_id = sqlc.arg(org_id) AND kind = sqlc.arg(kind) AND target_id = sqlc.arg(target_id)::uuid
  AND status = 'pending' AND expires_at > sqlc.arg(decided_at)::timestamptz;

-- name: LatestReviewRequests :many
-- The newest review of each of the items (the schedule and trigger pages
-- link it).
SELECT DISTINCT ON (target_id) id, target_id FROM run_requests
WHERE org_id = sqlc.arg(org_id) AND kind = sqlc.arg(kind) AND target_id = ANY(sqlc.arg(target_ids)::uuid[])
ORDER BY target_id, created_at DESC, id DESC;

-- name: GetLatestReviewRequest :one
-- The newest review of an item, whatever its state: a new review of an
-- item still paused for review carries over whether a person had paused it
-- before (fix round, final review 2).
SELECT * FROM run_requests
WHERE org_id = sqlc.arg(org_id) AND kind = sqlc.arg(kind) AND target_id = sqlc.arg(target_id)::uuid
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListOpenChangeReviews :many
-- The open reviews an assistant's change of what the template runs opened
-- (a schedule or trigger request's picture names them): those of the
-- items that run the template, and those whose changes name the template,
-- a template it starts through follow-ups (along any chain), the playbook
-- of one of them or that playbook's repository; for a repository sync
-- trigger, those naming the repository. Each with its item's name.
WITH RECURSIVE reached(id) AS (
  SELECT sqlc.narg(template_id)::uuid WHERE sqlc.narg(template_id)::uuid IS NOT NULL
  UNION
  SELECT f.follow_up_template_id FROM job_template_follow_ups f JOIN reached r ON f.template_id = r.id
  WHERE f.org_id = sqlc.arg(org_id)
), subjects(subject, id) AS (
  SELECT 'job_template'::text, id FROM reached
  UNION
  SELECT 'playbook', t.playbook_id FROM job_templates t JOIN reached r ON t.id = r.id
  WHERE t.org_id = sqlc.arg(org_id) AND t.playbook_id IS NOT NULL
  UNION
  SELECT 'repo', p.repo_id FROM job_templates t JOIN reached r ON t.id = r.id
  JOIN playbooks p ON p.org_id = t.org_id AND p.id = t.playbook_id
  WHERE t.org_id = sqlc.arg(org_id) AND p.repo_id IS NOT NULL
  UNION
  SELECT 'repo', sqlc.narg(repo_id)::uuid WHERE sqlc.narg(repo_id)::uuid IS NOT NULL
)
SELECT rr.id, rr.kind, COALESCE(s.name, j.name, '')::text AS item_name FROM run_requests rr
LEFT JOIN schedules s ON rr.kind = 'schedule_review' AND s.org_id = rr.org_id AND s.id = rr.target_id
LEFT JOIN job_triggers j ON rr.kind = 'trigger_review' AND j.org_id = rr.org_id AND j.id = rr.target_id
WHERE rr.org_id = sqlc.arg(org_id) AND rr.kind IN ('schedule_review', 'trigger_review')
  AND rr.status = 'pending' AND rr.expires_at > now()
  AND (rr.template_id = sqlc.narg(template_id)::uuid
    OR EXISTS (SELECT 1 FROM subjects sb
               WHERE rr.payload->'changes' @> jsonb_build_array(jsonb_build_object('subject', sb.subject, 'id', sb.id))))
ORDER BY rr.created_at, rr.id;

-- name: HasAutomation :one
-- Whether the organization has any schedule or any template trigger, in
-- whatever state: an assistant's inventory change has something to pause
-- (a stopped item gets its review too).
SELECT (EXISTS (SELECT 1 FROM schedules s WHERE s.org_id = $1)
    OR EXISTS (SELECT 1 FROM job_triggers t WHERE t.org_id = $1 AND t.kind = 'template_run'))::boolean AS has;
