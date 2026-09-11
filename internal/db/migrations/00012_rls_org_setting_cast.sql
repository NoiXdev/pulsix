-- +goose Up
-- Every org_isolation policy compared org_id against
-- current_setting('pulsix.org_id', true)::uuid. That cast is only safe while
-- the setting has never been touched on a connection: current_setting then
-- returns NULL. Once a transaction on a pooled connection has bound an
-- organization with set_config(..., true) — which every org-scoped request
-- now does, through internal/db's org scope — the placeholder exists for the
-- rest of that session and reverts to the empty string, not NULL, when the
-- transaction ends. The next instance-level statement on that connection then
-- failed with "invalid input syntax for type uuid" instead of falling through
-- to org_isolation_bypass: PostgreSQL gives no evaluation-order guarantee
-- between the two OR-combined permissive policies, so the bypass policy
-- matching does not stop the cast in the other one from being evaluated.
--
-- NULLIF maps the empty string back to NULL before the cast, so org_isolation
-- simply does not match an unbound connection and org_isolation_bypass
-- decides on its own, exactly as it always meant to. ALTER POLICY changes the
-- expression in place; no policy is dropped or renamed.
ALTER POLICY org_isolation ON org_memberships
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
ALTER POLICY org_isolation ON api_tokens
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
ALTER POLICY org_isolation ON invites
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
ALTER POLICY org_isolation ON audit_events
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
ALTER POLICY org_isolation ON zones
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
ALTER POLICY org_isolation ON hosts
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
ALTER POLICY org_isolation ON host_tags
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);

-- +goose Down
ALTER POLICY org_isolation ON org_memberships
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
ALTER POLICY org_isolation ON api_tokens
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
ALTER POLICY org_isolation ON invites
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
ALTER POLICY org_isolation ON audit_events
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
ALTER POLICY org_isolation ON zones
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
ALTER POLICY org_isolation ON hosts
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
ALTER POLICY org_isolation ON host_tags
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
