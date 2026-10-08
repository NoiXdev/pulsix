-- +goose Up
-- Backlog 1.30 and 1.31. hosts and host_tags predate the composite-key
-- convention migration 00014 established and every table since has
-- followed (workers 00017, jobs 00023, worker identities 00027, agent
-- tokens 00038, check samples 00044, alerts 00049): a row's tenant column
-- is tied to its owner's, so the database itself refuses a reference that
-- crosses organizations rather than trusting every writer to get it right.
--
-- No data repair runs here, deliberately. A row that violates either
-- constraint is the evidence of a bug — for host_tags, evidence that a tag
-- was visible to the wrong tenant — so the migration must fail loudly and
-- leave the rows to be looked at, not quietly rewrite them and destroy the
-- trace.

-- hosts.zone_id referenced zones(id) alone, so a host in one organization
-- could sit in another's zone. zones has carried UNIQUE (org_id, id) since
-- migration 00017, so only the constraint itself is new.
ALTER TABLE hosts
  ADD CONSTRAINT hosts_org_zone_fkey
  FOREIGN KEY (org_id, zone_id) REFERENCES zones (org_id, id);

-- The one that was actually load-bearing: host_tags.org_id is NOT NULL and
-- is what this table's row-level security policy filters on (migration
-- 00011), yet it referenced nothing — the column deciding who may see a tag
-- was free to disagree with the host the tag hangs off. The composite key
-- replaces the plain host_id reference, which it subsumes, and keeps the
-- same ON DELETE CASCADE so deleting a host still takes its tags.
ALTER TABLE host_tags DROP CONSTRAINT host_tags_host_id_fkey;
ALTER TABLE host_tags
  ADD CONSTRAINT host_tags_org_host_fkey
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE;

-- Backlog 1.31: the optimistic-concurrency counter If-Match compares
-- against, in the shape inventory_groups already uses. DEFAULT 1 gives
-- every existing host a starting revision, so nothing has to be backfilled.
ALTER TABLE hosts ADD COLUMN revision bigint NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE hosts DROP COLUMN revision;
ALTER TABLE host_tags DROP CONSTRAINT host_tags_org_host_fkey;
ALTER TABLE host_tags
  ADD CONSTRAINT host_tags_host_id_fkey
  FOREIGN KEY (host_id) REFERENCES hosts (id) ON DELETE CASCADE;
ALTER TABLE hosts DROP CONSTRAINT hosts_org_zone_fkey;
