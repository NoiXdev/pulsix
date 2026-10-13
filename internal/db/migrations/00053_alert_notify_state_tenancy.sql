-- +goose Up
-- Backlog 1.30, the same correction migration 00051 made for hosts and
-- host_tags. alert_notify_state was the one table migration 00052 created
-- with a single-column foreign key: its org_id is NOT NULL and is what the
-- table's own row-level security policy filters on, yet it referenced
-- nothing, while alert_event_id referenced alert_events(id) alone. The
-- column deciding who may see a decision was therefore free to disagree
-- with the event the decision is about — and the follower's forward pass
-- is an anti-join against this table, so a row with a mismatched org_id
-- would suppress ANOTHER organization's event from its own pass and that
-- event would never be looked at again.
--
-- Nothing in the code can produce such a row today: only the follower
-- writes this table and it always passes the organization it read the
-- event from. That is an argument for fixing it now rather than for
-- leaving it — a tenancy boundary that holds only because of what the
-- current callers happen to do is exactly the kind backlog 1.30 spent a
-- migration removing elsewhere.
--
-- 00052 is not edited: migrations here are additive only, and that one has
-- been applied.

-- alert_events predates the composite-key convention for its own
-- referents: it carries PRIMARY KEY (id) and no UNIQUE (org_id, id), so
-- there is nothing for a composite foreign key to point at yet. Adding it
-- is free — (org_id, id) is unique the moment id alone is.
ALTER TABLE alert_events
  ADD CONSTRAINT alert_events_org_id_key UNIQUE (org_id, id);

-- No data repair, for migration 00051's own reason: a row that violates
-- the new constraint is the evidence of a bug — a decision recorded
-- against another tenant's event — so this must fail loudly and leave the
-- row to be looked at rather than quietly rewrite it and destroy the
-- trace. alert_event_id stays the primary key; the composite foreign key
-- subsumes the single-column one it replaces, and keeps the same ON DELETE
-- CASCADE so the ledger still ages out with the history it describes.
ALTER TABLE alert_notify_state DROP CONSTRAINT alert_notify_state_alert_event_id_fkey;
ALTER TABLE alert_notify_state
  ADD CONSTRAINT alert_notify_state_org_event_fkey
  FOREIGN KEY (org_id, alert_event_id) REFERENCES alert_events (org_id, id) ON DELETE CASCADE;

-- No table is created here, so there is no row-level security to enable
-- and no org_isolation / org_isolation_bypass pair to add: both tables
-- already carry theirs (alert_events from 00049, alert_notify_state from
-- 00052), and TestEveryOrgOwnedTableForcesRowLevelSecurity keeps that true
-- whatever a later migration does.

-- +goose Down
ALTER TABLE alert_notify_state DROP CONSTRAINT alert_notify_state_org_event_fkey;
ALTER TABLE alert_notify_state
  ADD CONSTRAINT alert_notify_state_alert_event_id_fkey
  FOREIGN KEY (alert_event_id) REFERENCES alert_events (id) ON DELETE CASCADE;
ALTER TABLE alert_events DROP CONSTRAINT alert_events_org_id_key;
