-- +goose Up
-- Backlog 2.113. Since backlog 2.107 a cancel stops the play on the worker,
-- and the run view stopped warning that the playbook may still be running.
-- That holds only for a worker that runs plays under the play supervisor:
-- a worker older than 2.107, or one off Linux, still only stops following
-- the play, and it keeps running. The worker now says which it is on every
-- claim (ClaimJobRequest.cancel_stops_play), and this column records the
-- answer of the worker that claimed the job, written by the ClaimJob UPDATE
-- itself, so a worker upgraded after the run cannot change what its run
-- view says about it.
--
-- NULL means "nobody said": a job never claimed, one claimed before this
-- migration, one claimed by the manager's own in-process path, or one a
-- requeue handed back (the reaper's and a revocation's requeues clear it
-- with worker_id). The run view treats NULL like false, the cautious
-- answer, and keeps the stronger warning.
ALTER TABLE jobs ADD COLUMN cancel_stops_play boolean;

-- +goose Down
ALTER TABLE jobs DROP COLUMN cancel_stops_play;
