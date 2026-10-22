-- +goose Up
-- worker_enrollments.worker_id (00027) carried no foreign key, so a worker
-- row could be deleted (workers.DeleteWorker) while an enrollment still
-- pointed at its id. The enrollment is the record of how that worker
-- joined; it should outlive the worker, not be forced into lockstep with
-- it, so the right action is SET NULL rather than CASCADE — the row stays,
-- it just stops pointing at a worker that no longer exists. workers
-- already carries the composite UNIQUE (org_id, id) this needs (00017).
--
-- The column list on SET NULL (PostgreSQL 15+) matters here: a composite
-- FK's default SET NULL clears every column in the FK, which would also
-- null out org_id — itself NOT NULL — and abort the delete. Naming
-- worker_id clears only that column.
ALTER TABLE worker_enrollments
  ADD CONSTRAINT worker_enrollments_worker_fk
  FOREIGN KEY (org_id, worker_id) REFERENCES workers (org_id, id) ON DELETE SET NULL (worker_id);

-- created_by intentionally gets no foreign key here, matching the
-- convention check_definitions (00043) and check_item_decisions (00048)
-- already document for their own created_by: the audit log is the durable
-- record of who did this, not a referential identity this table has to
-- keep valid as users come and go.

-- +goose Down
ALTER TABLE worker_enrollments DROP CONSTRAINT worker_enrollments_worker_fk;
