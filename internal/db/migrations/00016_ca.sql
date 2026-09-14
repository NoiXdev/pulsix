-- +goose Up
-- The instance's internal CA, sealed with the master key (spec §14). One row
-- per instance; Ensure() creates it on first start.
CREATE TABLE instance_ca (
  id         integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  sealed     bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE instance_ca;
