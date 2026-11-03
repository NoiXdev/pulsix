-- +goose Up
-- The agent certificate's key (backlog 7.47): the hex SHA-256 of the
-- leaf's SubjectPublicKeyInfo. An agent keeps its key across renewals
-- (internal/agent's LoadOrGenerateKey), so after a restore the manager
-- adopts a certificate the restored database does not know only when it
-- carries the same key as the host's newest registered certificate — a
-- different host enrolled under the same agent name cannot take the
-- identity over.
--
-- Additive and nullable, with no backfill: the manager never kept the
-- leaf, so a row written before this migration has no key to state, and a
-- host whose newest certificate has none is simply never adopted into.
ALTER TABLE agent_certificates ADD COLUMN public_key_sha256 text;

-- +goose Down
ALTER TABLE agent_certificates DROP COLUMN public_key_sha256;
