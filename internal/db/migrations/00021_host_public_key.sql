-- +goose Up
-- Full host public keys next to their fingerprints (final review C2). A pin
-- that is only a fingerprint cannot be enforced by ansible: OpenSSH's
-- known_hosts needs the key itself. The connection test now reports the
-- observed key in authorized-key form ("ssh-ed25519 AAAA…"), each test row
-- keeps it, and pinning copies it onto host_ssh together with the
-- fingerprint.
--
-- No backfill is possible: a host pinned before this migration carries a
-- fingerprint only. Its pin is not enforceable until the next connection
-- test observes a key whose fingerprint equals the pin — that test fills
-- ssh_host_public_key automatically (hosts.Service.RecordConnectionTest);
-- until then a run, ad-hoc, bootstrap or rotation claim for the host fails
-- with an error asking for a connection test (the daily re-test closes the
-- gap on its own within a day).
ALTER TABLE host_ssh ADD COLUMN ssh_host_public_key text;
ALTER TABLE host_connection_tests ADD COLUMN host_public_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE host_connection_tests DROP COLUMN host_public_key;
ALTER TABLE host_ssh DROP COLUMN ssh_host_public_key;
