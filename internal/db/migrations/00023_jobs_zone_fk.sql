-- +goose Up
-- A job's zone is a zone of the job's own organization (final review
-- triage): the composite foreign key every org-owned reference uses. The
-- claim already filters on the worker's organization; the constraint makes
-- a job queued into another organization's zone impossible at the source.
ALTER TABLE jobs ADD CONSTRAINT jobs_org_zone_fkey FOREIGN KEY (org_id, zone_id) REFERENCES zones (org_id, id);

-- +goose Down
ALTER TABLE jobs DROP CONSTRAINT jobs_org_zone_fkey;
