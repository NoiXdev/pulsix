-- +goose Up

-- v0.4d escalation-chains review: records the moment an alert's severity
-- column last moved, rise or fall alike (RaiseAlertSeverity, queries/
-- alerts.sql, sets it on every call — "the only reason Reconcile emits
-- this action at all" is that the column is about to move). A FALLING
-- severity writes no alert_events row at all (raiseSeverity's own comment,
-- internal/alerts/store_sql.go — "the column moves, the history does not
-- gain noise"), so this is the only trace a fall leaves anywhere, and a
-- real, honest fact about an alert worth having a column for regardless
-- of how escalation does or doesn't use it.
--
-- escalatePass (internal/alertroutes) deliberately does NOT anchor a
-- chain's resume timing on this column. An earlier version of this
-- migration's own comment argued it should — "the same anchor role
-- severity_raised already plays, extended to cover a fall too" — and a
-- round of review built exactly that fold, then caught the reasoning
-- failing on its own terms: this column moves on EVERY severity write,
-- including one that never leaves a route's own severity set at all (crit
-- down to warn on a {warn, crit} route still moves it). Folding it into a
-- chain's resume anchor restarted the next rung's delay on every such
-- wobble, which can stall a chain oscillating inside the set forever — a
-- burst pages someone a few minutes early, a stall pages nobody at all,
-- which is the one outcome the whole feature exists to prevent. The spec
-- was amended to match: a severity excursion is not a mute (nobody was
-- told anything during a mute; during an excursion, other routes may well
-- have been shouting the whole time), so re-entering a route's severity
-- set fires the next rung promptly rather than waiting out a fresh delay,
-- and this column plays no part in that.
--
-- Added nullable, backfilled, then set NOT NULL: a bare ADD COLUMN ...
-- NOT NULL with no DEFAULT fails immediately against every existing row,
-- and this table is not expected to be empty on any installation that has
-- reached v0.4d. The backfill reads each row's own opened_at — an alert
-- that predates this column behaves exactly as if its severity had last
-- changed the moment it opened, which is the only honest reading for a row
-- whose real severity history this column was never there to record.
--
-- This is the heavier of the two migrations touching alerts this release.
-- 00056's own warning already named the ACCESS EXCLUSIVE lock its own
-- ADD CONSTRAINT ... UNIQUE takes while building the backing index; this
-- migration pays that same lock TWICE, with a full-table UPDATE sitting
-- between the two. The ADD COLUMN above is metadata-only and fast (no
-- DEFAULT, so nothing to backfill at add time), but the UPDATE that
-- follows rewrites every row in the table — alerts carries no retention
-- policy (00049's own "No retention policy") and only grows, so on a
-- long-running installation this is a real cost, not a formality — and the
-- SET NOT NULL after it takes a second ACCESS EXCLUSIVE lock plus a full
-- scan to prove no row was left NULL, with no way to skip that scan since
-- no prior CHECK constraint already established it. An operator applying
-- this migration on a large installation should expect a startup that
-- looks hung, with alerting stopped for its duration, not be surprised by
-- one.
ALTER TABLE alerts ADD COLUMN severity_changed_at timestamptz;
UPDATE alerts SET severity_changed_at = opened_at;
ALTER TABLE alerts ALTER COLUMN severity_changed_at SET NOT NULL;

-- +goose Down
ALTER TABLE alerts DROP COLUMN severity_changed_at;
