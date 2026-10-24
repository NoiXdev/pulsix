-- +goose Up
-- Backlog 2.81's remainder. A schedule with the exclude_hosts
-- maintenance guard drops the hosts in a window before the jobs are
-- created, so the run's own numbers only ever describe what remained:
-- the message read "9 hosts (ok 9, changed 0, failed 0)" with no hint
-- that three more were left out. The aggregate query cannot recover it
-- -- RunRequest.ExcludeHosts shapes the targets at enqueue time and was
-- never written down anywhere.
--
-- The count is the REQUEST's, not the zone's, and every job of a run
-- group carries the same number: a zone all of whose hosts were
-- excluded produces no job at all, so a sum over jobs would silently
-- under-report exactly the case with the most to report. The group
-- query reads it with max().
ALTER TABLE jobs ADD COLUMN hosts_excluded integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE jobs DROP COLUMN hosts_excluded;
