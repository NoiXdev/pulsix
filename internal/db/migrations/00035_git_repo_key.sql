-- +goose Up
-- Git server host-key pins (backlog 2.41), mirroring the host model of
-- migration 00021 and the bastion model of 00034: a repository's pin is the
-- OpenSSH fingerprint of its git server's host key plus the full public key
-- in authorized-key form — the fingerprint is what the read APIs show and
-- compare, the key is what the worker writes into a run's known_hosts so the
-- fetch's own ssh connection enforces the pin (StrictHostKeyChecking=yes).
--
-- The pin is written by the manager's sync sink (RecordSyncResult) from the
-- key the worker observed on the FIRST (unpinned) successful sync — trust on
-- first use per repository; a sync that succeeded against a changed key can
-- never happen (the worker's strict check refuses the changed key before any
-- data is transferred), so the sink never needs to overwrite a pin.
--
-- No backfill is possible: repositories pinned before this migration carry
-- nothing (the column starts NULL = unpinned, trust on first use) — the next
-- successful sync pins the observed key.
ALTER TABLE git_repos ADD COLUMN ssh_host_key text CHECK (ssh_host_key IS NULL OR ssh_host_key LIKE 'SHA256:%');
ALTER TABLE git_repos ADD COLUMN ssh_host_public_key text;

-- +goose Down
ALTER TABLE git_repos DROP COLUMN ssh_host_public_key;
ALTER TABLE git_repos DROP COLUMN ssh_host_key;
