-- +goose Up
-- Jump-host (bastion) host-key pins (backlog 2.40), mirroring the host model
-- of migration 00021: a bastion pin is the OpenSSH fingerprint plus the full
-- public key in authorized-key form — the fingerprint is what the connection
-- test's probe compares, the key is what the worker writes into a run's
-- known_hosts so ansible's own SSH connections enforce the pin.
--
-- Every connection test that dials a host through a jump chain observes each
-- hop's host key; the test row keeps them (hop_keys jsonb, one entry per
-- observed hop) so the operator can pin what a test saw and so the runner's
-- claim can refuse routes through a bastion whose pinned key no longer
-- matches its latest observation (fail closed, like a changed host key).
--
-- No backfill is possible: a bastion pinned before this migration carries
-- nothing (the column starts NULL = unpinned, trust on first use) — the pin
-- is made from a connection test's reported hop keys via the pin API.
ALTER TABLE bastions ADD COLUMN ssh_host_key text CHECK (ssh_host_key IS NULL OR ssh_host_key LIKE 'SHA256:%');
ALTER TABLE bastions ADD COLUMN ssh_host_public_key text;
ALTER TABLE host_connection_tests ADD COLUMN hop_keys jsonb NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE host_connection_tests DROP COLUMN hop_keys;
ALTER TABLE bastions DROP COLUMN ssh_host_public_key;
ALTER TABLE bastions DROP COLUMN ssh_host_key;
