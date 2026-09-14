-- name: GetInstanceCA :one
-- The sealed internal CA blob (internal/ca.Service.Ensure); absent before
-- the first start generated and stored it.
SELECT sealed FROM instance_ca WHERE id = 1;

-- name: UpsertInstanceCA :exec
-- Stores or replaces the sealed CA blob of the single row.
INSERT INTO instance_ca (id, sealed) VALUES (1, $1)
ON CONFLICT (id) DO UPDATE SET sealed = EXCLUDED.sealed;
