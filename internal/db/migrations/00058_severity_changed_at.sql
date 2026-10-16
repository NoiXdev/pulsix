-- +goose Up

-- v0.4d escalation-chains review, Critical 2: a FALLING severity writes no
-- alert_events row at all (raiseSeverity's own comment, internal/alerts/
-- store_sql.go — "the column moves, the history does not gain noise"), so
-- an alert that rises out of a route's severity set and later falls back
-- into it had no anchor whatsoever to measure a resumed chain's next
-- delay from: severity_raised only ever fires on a RISE, and the acceptance
-- list (spec docs/superpowers/specs/2026-10-16-v0.4d-escalation-chains-
-- design.md) names this exact case — "an alert that leaves a route's
-- severity set and re-enters it resumes at the next unfired step, and that
-- step gets its full delay from the moment of re-entry" — without an
-- anchor a falling-then-rising severity can ever supply.
--
-- severity_changed_at closes that gap: set on EVERY severity write, rise
-- or fall alike (RaiseAlertSeverity, queries/alerts.sql), it is "the
-- moment this alert last became urgent" regardless of which direction
-- brought the alert back into a route's selection — the same anchor role
-- severity_raised already plays for a rise, extended to cover a fall too.
--
-- Added nullable, backfilled, then set NOT NULL: a bare ADD COLUMN ...
-- NOT NULL with no DEFAULT fails immediately against every existing row,
-- and this table is not expected to be empty on any installation that has
-- reached v0.4d. The backfill reads each row's own opened_at — an alert
-- that predates this column behaves exactly as if its severity had last
-- changed the moment it opened, which is the only honest reading for a row
-- whose real severity history this column was never there to record.
ALTER TABLE alerts ADD COLUMN severity_changed_at timestamptz;
UPDATE alerts SET severity_changed_at = opened_at;
ALTER TABLE alerts ALTER COLUMN severity_changed_at SET NOT NULL;

-- +goose Down
ALTER TABLE alerts DROP COLUMN severity_changed_at;
