-- +goose Up
-- host_tags carries both kinds of tag a host can have: "manual" (set by a
-- person through PUT /orgs/{org}/hosts/{id}/tags) and "fact" (derived
-- automatically from what the agent reports — a later task's concern; this
-- migration only reserves the source value). The primary key includes
-- source, not just (host_id, key), precisely so a manual tag and a fact tag
-- can coexist on the same key without either one clobbering the other at
-- the storage layer — internal/hosts.Service.SetTags replaces only the
-- manual rows for a host, leaving its fact rows untouched, and a reader
-- that wants the effective value merges the two with manual winning (see
-- internal/hosts/selector's own doc comment on how SQL() expects that merge
-- to be provided).
CREATE TABLE host_tags (
  host_id uuid NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  org_id  uuid NOT NULL,
  key     text NOT NULL,
  value   text NOT NULL,
  source  text NOT NULL CHECK (source IN ('manual', 'fact')),
  PRIMARY KEY (host_id, key, source)
);
-- Serves both ListTagKeyValueCounts (GET /orgs/{org}/tags, grouped by
-- key/value for autocomplete) and the selector.SQL() EXISTS sub-query
-- pattern (t.key = $N [AND t.value = $N]), each scoped to one organization.
CREATE INDEX host_tags_org_key_value ON host_tags(org_id, key, value);
-- RLS for host_tags, the same org_isolation / org_isolation_bypass pair
-- every other org-owned table in this schema carries (see hosts' own
-- migration, 00010_hosts.sql, for the pattern this mirrors).
ALTER TABLE host_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE host_tags FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON host_tags
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
CREATE POLICY org_isolation_bypass ON host_tags
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE host_tags;
