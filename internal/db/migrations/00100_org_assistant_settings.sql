-- +goose Up
-- The per-organization assistant access switch (spec docs/superpowers/
-- specs/2026-11-16-assistant-access-design.md, "Assistant access switch").
-- One row per organization, an absent row meaning off: assistant access is
-- opt-in. The row-level security pair is the standard form (00033).
CREATE TABLE org_assistant_settings (
  org_id     uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  enabled    boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE org_assistant_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_assistant_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON org_assistant_settings
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON org_assistant_settings
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE org_assistant_settings;
