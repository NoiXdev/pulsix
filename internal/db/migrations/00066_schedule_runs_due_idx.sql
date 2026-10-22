-- +goose Up
-- ListScheduleRunsBetween (the calendar's past occurrences and the
-- digest's week) filters org_id + a due_at range, but schedule_runs
-- (00025) only ever indexed (org_id, schedule_id, decided_at DESC) — built
-- for the per-schedule runs list, not this query. Every digest build and
-- every calendar page view scanned the organization's whole run history.
CREATE INDEX schedule_runs_org_due_idx ON schedule_runs (org_id, due_at);

-- +goose Down
DROP INDEX schedule_runs_org_due_idx;
