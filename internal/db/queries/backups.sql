-- name: InsertBackupRun :one
-- A scheduled attempt claims its slot here: when another manager already
-- holds the slot nothing is inserted and no row comes back. An abandoned
-- attempt does not hold its slot; the conflict target names the same
-- predicate as the partial unique index.
INSERT INTO backup_runs (kind, slot, started_at, state, object_name, actor_id)
VALUES (sqlc.arg(kind), sqlc.narg(slot), sqlc.arg(started_at), 'running', sqlc.arg(object_name), sqlc.narg(actor_id))
ON CONFLICT (slot) WHERE kind = 'scheduled' AND state <> 'abandoned' DO NOTHING
RETURNING id;

-- name: FinishBackupRun :exec
UPDATE backup_runs
SET state = sqlc.arg(state), finished_at = sqlc.arg(finished_at), size_bytes = sqlc.narg(size_bytes), error = sqlc.narg(error)
WHERE id = sqlc.arg(id) AND state = 'running';

-- name: AbandonRunningBackupRuns :execrows
UPDATE backup_runs
SET state = 'abandoned', finished_at = sqlc.arg(finished_at), error = sqlc.arg(error)
WHERE state = 'running';

-- name: AnyBackupRunning :one
SELECT EXISTS (SELECT 1 FROM backup_runs WHERE state = 'running');

-- name: BackupSlotClaimed :one
SELECT EXISTS (SELECT 1 FROM backup_runs WHERE kind = 'scheduled' AND state <> 'abandoned' AND slot = sqlc.arg(slot));

-- name: ListRecentBackupRuns :many
SELECT r.id, r.kind, r.slot, r.started_at, r.finished_at, r.state, r.object_name, r.size_bytes, r.error, r.actor_id,
       COALESCE(u.display_name, '')::text AS actor_name
FROM backup_runs r
LEFT JOIN users u ON u.id = r.actor_id
ORDER BY r.started_at DESC, r.id DESC
LIMIT sqlc.arg(max_rows);

-- name: ListInstanceAdminEmails :many
-- Who hears about a failed scheduled backup: every enabled instance administrator.
SELECT email FROM users WHERE instance_admin AND disabled_at IS NULL ORDER BY email;

-- name: ListInterruptedBackupSlots :many
-- The scheduled slots before the given one whose attempts were all
-- interrupted and that nobody was told about yet, each with its newest
-- attempt. A slot with an attempt that is running, succeeded or failed is
-- left out (a failure is mailed when it happens), and so is one with an
-- attempt a restore abandoned (error = restored_reason): that attempt is
-- the backup the restore came from, which did complete.
SELECT DISTINCT ON (r.slot) r.id, r.slot
FROM backup_runs r
WHERE r.kind = 'scheduled' AND r.slot < sqlc.arg(before)
  AND NOT EXISTS (
    SELECT 1 FROM backup_runs o
    WHERE o.kind = 'scheduled' AND o.slot = r.slot
      AND (o.state <> 'abandoned' OR o.notified_at IS NOT NULL OR o.error IS NOT DISTINCT FROM sqlc.arg(restored_reason)::text)
  )
ORDER BY r.slot, r.started_at DESC, r.id DESC;

-- name: MarkBackupRunNotified :execrows
-- Records the interrupted-slot mail on the slot's newest attempt. No row
-- comes back changed when another manager marked it first; only the one
-- that marked it sends the mail.
UPDATE backup_runs SET notified_at = sqlc.arg(notified_at)
WHERE id = sqlc.arg(id) AND notified_at IS NULL;
