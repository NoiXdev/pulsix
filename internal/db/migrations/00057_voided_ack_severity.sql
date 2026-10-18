-- +goose Up
-- v0.4d decision 2 (backlog 4.36): acknowledged_severity is now cleared in
-- the same UPDATE that writes an ack_voided event (RaiseAlertSeverity,
-- queries/alerts.sql), while acknowledged_at/acknowledged_by stay — the
-- acknowledgement itself still happened, it just no longer covers. That is
-- exactly the state 00049's own unnamed CHECK (named alerts_check1 by
-- Postgres's own convention, the same way 00050's own comment names
-- maintenance_windows_target_kind_check) forbids:
-- CHECK ((acknowledged_at IS NULL) = (acknowledged_severity IS NULL)) ties
-- the two together in both directions, so acknowledged_at set with
-- acknowledged_severity NULL — a voided ack — violates it.
--
-- The invariant worth keeping is only the half that still makes sense: a
-- severity can never be recorded without an ack_at to go with it (there is
-- no "acknowledged at severity X" with no acknowledgement). The reverse
-- no longer holds. Dropping and re-adding the unnamed CHECK only relaxes
-- it, the same move 00032 and 00050 both already made for a narrower
-- CHECK that later needed to widen: no existing row's columns change, so
-- no row can violate the new, weaker constraint.
ALTER TABLE alerts
  DROP CONSTRAINT alerts_check1,
  ADD CONSTRAINT alerts_ack_severity_implies_ack_at
    CHECK (acknowledged_severity IS NULL OR acknowledged_at IS NOT NULL);

-- +goose Down
-- NOT VALID, the same reasoning 00032's and 00050's own Down sections give:
-- an alert actually left in the voided shape (acknowledged_at set,
-- acknowledged_severity NULL) — the ordinary result of this feature firing
-- even once — would make a plain re-add of the old CHECK fail immediately.
-- The narrower CHECK applies to new rows only, so the rollback never fails
-- and deletes nothing.
ALTER TABLE alerts
  DROP CONSTRAINT alerts_ack_severity_implies_ack_at,
  ADD CONSTRAINT alerts_check1
    CHECK ((acknowledged_at IS NULL) = (acknowledged_severity IS NULL)) NOT VALID;
