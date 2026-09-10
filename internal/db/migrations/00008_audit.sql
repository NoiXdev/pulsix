-- +goose Up
-- audit_events records every successful mutating API call, plus (from later
-- tasks) system-generated events with no user behind them at all — hence
-- actor_id being nullable while actor_kind is always set (it distinguishes
-- "user", "token" and "system", see internal/audit.ActorKind).
--
-- org_id is nullable for the same reason a handful of routes are not
-- scoped to an organization (POST /orgs, instance-admin actions): those
-- either have no org yet or act above the org level entirely. A null
-- org_id row satisfies neither RLS policy's USING clause when
-- pulsix.org_id is set to some concrete org (it is never NULL = that org,
-- nor does the bypass clause apply), so it never leaks into an org's own
-- activity feed — by design, since it isn't that org's event.
CREATE TABLE audit_events (
  id          uuid PRIMARY KEY,
  org_id      uuid REFERENCES organizations(id) ON DELETE CASCADE,
  actor_id    uuid,
  actor_kind  text NOT NULL,
  action      text NOT NULL,
  object_type text NOT NULL,
  object_id   text NOT NULL,
  summary     text NOT NULL,
  meta        jsonb NOT NULL DEFAULT '{}',
  at          timestamptz NOT NULL
);
-- Serves both the org-scoped feed (ORDER BY at DESC, id DESC, keyset
-- pagination on org_id) and, as a prefix, plain org_id lookups.
CREATE INDEX audit_events_org_at ON audit_events(org_id, at DESC, id DESC);
-- Serves /me/activity, which lists one actor's events across every org —
-- it cannot use the org-scoped index above at all since it has no org_id
-- to filter on.
CREATE INDEX audit_events_actor_at ON audit_events(actor_id, at DESC, id DESC);

ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON audit_events USING (org_id = current_setting('pulsix.org_id', true)::uuid);
CREATE POLICY org_isolation_bypass ON audit_events USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE audit_events;
