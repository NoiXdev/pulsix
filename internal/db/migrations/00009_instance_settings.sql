-- +goose Up
-- instance_settings stores instance-wide configuration as JSON blobs keyed
-- by a short name ("smtp" today, "branding" later). Any secret inside a
-- value (the SMTP password) is sealed with the instance master key
-- (internal/crypto.Box) before it ever reaches this table — see
-- internal/setup.Service.SaveSMTP — so a database dump alone never exposes
-- it in the clear.
CREATE TABLE instance_settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
DROP TABLE instance_settings;
