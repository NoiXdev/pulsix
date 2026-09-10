-- +goose Up
-- +goose StatementBegin
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pulsix_app') THEN
    CREATE ROLE pulsix_app NOLOGIN;
  END IF;
END $$;
-- +goose StatementEnd
-- The login role in PULSIX_DATABASE_URL must be able to SET ROLE into
-- pulsix_app; a superuser can do that anyway, a plain owner needs the grant.
GRANT pulsix_app TO current_user;
GRANT USAGE ON SCHEMA public TO pulsix_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO pulsix_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO pulsix_app;

ALTER TABLE org_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON org_memberships
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
CREATE POLICY org_isolation_bypass ON org_memberships
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
-- Roles and their grants are cluster-scoped, not database-scoped, so this
-- down migration deliberately leaves pulsix_app and its privileges in place
-- rather than dropping a role other databases in the cluster may rely on.
DROP POLICY org_isolation_bypass ON org_memberships;
DROP POLICY org_isolation ON org_memberships;
ALTER TABLE org_memberships NO FORCE ROW LEVEL SECURITY;
ALTER TABLE org_memberships DISABLE ROW LEVEL SECURITY;
