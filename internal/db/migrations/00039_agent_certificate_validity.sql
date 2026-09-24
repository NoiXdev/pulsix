-- +goose Up
-- The agent certificate's validity window, for display: the Agents page
-- and the host's Agent card show when each agent's certificate expires,
-- and nothing else in the system knows the leaf — the manager issues it,
-- hands it to the agent and keeps only the serial (00037). The worker
-- side carries the same two columns since 00027; this brings the agent
-- registry level with it.
--
-- Additive and nullable: a row written before this migration has no
-- window to state, and the backfill below can only approximate one. The
-- service fills both columns on every Register from the issued leaf, so
-- every certificate from here on carries its real window.
ALTER TABLE agent_certificates ADD COLUMN not_before timestamptz;
ALTER TABLE agent_certificates ADD COLUMN not_after timestamptz;

-- Existing rows keep a usable answer instead of an empty one: the agent
-- leaf is 90 days (internal/ca), issued when the row was written. This is
-- an approximation used for display only — the agent renews from its real
-- certificate, never from this.
UPDATE agent_certificates
SET not_before = created_at, not_after = created_at + interval '90 days'
WHERE not_before IS NULL;

-- +goose Down
ALTER TABLE agent_certificates DROP COLUMN not_after;
ALTER TABLE agent_certificates DROP COLUMN not_before;
