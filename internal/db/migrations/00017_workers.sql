-- +goose Up
-- Zone workers: the probes this manager's gRPC endpoint talks to (spec §6).
-- A worker is identified by the serial of the client certificate the
-- instance CA issued it; the serial is stored as its decimal string,
-- because a big.Int has neither a uuid nor a numeric sqlc mapping and the
-- serial is opaque to SQL.
--
-- GetBySerial/Touch run outside the pulsix.org_id scope (the gRPC
-- interceptor has no org context; the serial is the secret), so this table
-- deliberately carries no row-level security: a serial is globally unique
-- and belongs to exactly one row, and every org-scoped statement filters by
-- org_id in its WHERE clause. All other statements (Register/List/Delete)
-- run through *db.DB org-scoping exactly like every other store.
--
-- The composite foreign key below needs zones to be unique per (org_id,
-- id), the constraint every org-owned child table targets since 00014 —
-- zones only had UNIQUE (org_id, slug) until now, so this migration adds
-- the missing constraint (additive). It must precede the FK's table.
ALTER TABLE zones ADD CONSTRAINT zones_org_id_id_key UNIQUE (org_id, id);

CREATE TABLE workers (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  zone_id     uuid NOT NULL,
  name        text NOT NULL,
  cert_serial text NOT NULL,
  last_seen   timestamptz,
  version     text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  UNIQUE (cert_serial),
  FOREIGN KEY (org_id, zone_id) REFERENCES zones (org_id, id)
);

-- +goose Down
ALTER TABLE zones DROP CONSTRAINT zones_org_id_id_key;
DROP TABLE workers;
