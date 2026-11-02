-- name: InsertBackupRun :one
-- A scheduled attempt claims its slot here: when another manager already
-- holds the slot nothing is inserted and no row comes back.
INSERT INTO backup_runs (kind, slot, started_at, state, object_name, actor_id)
VALUES (sqlc.arg(kind), sqlc.narg(slot), sqlc.arg(started_at), 'running', sqlc.arg(object_name), sqlc.narg(actor_id))
ON CONFLICT (slot) WHERE kind = 'scheduled' DO NOTHING
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
SELECT EXISTS (SELECT 1 FROM backup_runs WHERE kind = 'scheduled' AND slot = sqlc.arg(slot));

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
