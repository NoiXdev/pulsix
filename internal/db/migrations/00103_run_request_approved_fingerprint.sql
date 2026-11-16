-- +goose Up
-- The fingerprint a person approved (spec 2026-11-16 assistant access,
-- "Run requests"). Migration 00102 kept none because an approval always
-- recomputes it; the claim of an approved run now recomputes it again and
-- compares it with this one, so a change between the approval and the
-- claim fails the run instead of running something nobody saw. Null until
-- the request is approved, and again after a failed enqueue put it back to
-- pending. An approval also sets run_group_id to the request's own id
-- before anything is enqueued, so the claim always finds its request.
ALTER TABLE run_requests ADD COLUMN approved_fingerprint text;

-- +goose Down
ALTER TABLE run_requests DROP COLUMN approved_fingerprint;
