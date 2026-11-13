-- +goose Up
-- Client hosts (backlog 3.57, spec
-- docs/superpowers/specs/2026-11-13-client-hosts-design.md). A host is a
-- server (always on: an absent agent is a problem) or a client (a notebook
-- or desktop that is off by design: while its agent is not online the
-- evaluation freezes it instead of turning it unknown).
--
-- Every existing host stays a server. The constant default makes the ADD
-- COLUMN a catalog-only change (no table rewrite); the CHECK then validates
-- the existing rows with one scan, and every one of them reads 'server'.
-- An older release lists the columns it knows, so it neither reads nor
-- writes kind; a host it creates is a server.
ALTER TABLE hosts ADD COLUMN kind text NOT NULL DEFAULT 'server';
ALTER TABLE hosts ADD CONSTRAINT hosts_kind_check CHECK (kind IN ('server', 'client'));

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE hosts DROP CONSTRAINT hosts_kind_check;
ALTER TABLE hosts DROP COLUMN kind;
