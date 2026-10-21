-- +goose Up
-- A customer may rebind a shared check definition's projection to a
-- monitoring group or a host (migration 00062, spec v0.2f decision 2) — and
-- both of those bindings are ON DELETE CASCADE off monitoring_groups and
-- hosts (migration 00043:59-61): "deleting a group or host takes its
-- bindings with it" is exactly right for a customer's OWN check, but for a
-- SHARED projection it is spec decision 6 broken through a side door —
-- nothing in checks.Service.DeleteGroup or hosts.Service.DeleteHost ever
-- calls checks.Service.DeleteDefinition, so that function's own
-- origin == "shared" refusal (added in this same migration's own task)
-- never runs at all. The cascade is a database action against
-- check_definitions directly; a customer deleting their own group or host
-- would otherwise silently and permanently destroy the provider's check for
-- that organization, with no trace and no route to discovery — precisely
-- the spec's own risk list: "a projection drifting from its definition."
--
-- Review ruling on the fix: do NOT refuse the group/host deletion — a
-- customer must stay in control of their own fleet, and letting an
-- installation-owned row block that inverts this whole design's ownership
-- split. Instead, REBIND the projection back to the ('org', NULL, NULL)
-- default it was born at (shared.ProjectedCheckColumns' own default,
-- internal/shared) rather than letting it be deleted: the customer loses
-- the binding choice they themselves just destroyed, not the provider's
-- check.
--
-- Fixed at the database, not in Go, because that is the one place neither
-- checks.Service.DeleteGroup, hosts.Service.DeleteHost, nor any THIRD
-- deletion path written later can route around: a BEFORE DELETE trigger on
-- check_definitions itself that detects exactly this cascade and rewrites
-- the row instead of letting it be removed.
--
-- Detecting "this delete is a cascade from MY OWN group or host dying" is
-- the one subtle part, and it was verified empirically against a real
-- Postgres instance before being written here, not assumed:
--
--   - pg_trigger_depth() alone does NOT work, even though it looks like
--     the obvious tool. Measured directly: a plain top-level
--     `DELETE FROM check_definitions ...` (internal/shared's own
--     DeleteProjectedCheckDefinition, removing a projection the PROVIDER
--     asked to delete) reports depth 1 inside this trigger, not 0 — the
--     currently-executing trigger counts itself. A cascade nested inside
--     the FK's own internal RI delete trigger reports depth 2. So
--     `pg_trigger_depth() > 1` DOES correctly tell a cascade apart from a
--     direct delete... for group_id/host_id's own cascade. It does NOT
--     tell a group/host cascade apart from check_definitions.org_id's OWN
--     cascade (migration 00043, ON DELETE CASCADE off organizations) —
--     that path is *also* nested one level inside organizations' own RI
--     trigger, same depth, same condition. Verified by reproducing it: a
--     trigger gated on depth alone left three orphaned check_definitions
--     rows referencing an organization that `DELETE FROM organizations`
--     had already removed, with no error raised at all — Postgres does not
--     re-verify that a cascade actually emptied the child table once an
--     application trigger has silently no-op'ed part of it. That is a
--     worse bug than the one this migration fixes, which is why this
--     comment records the dead end rather than only the final answer: a
--     future "optimization" back toward pg_trigger_depth() alone would
--     reintroduce it.
--   - The condition actually used asks a narrower, structural question
--     instead of "how deep in the trigger stack am I": is MY OWN
--     organization still here, and is MY OWN binding target (the specific
--     group or host this row names) now gone? Both must hold for this to
--     be the cascade this migration exists to catch:
--       * organizations WHERE id = OLD.org_id still exists — rules out
--         org_id's own cascade (where the organization is already gone by
--         the time this trigger fires, in the same transaction, and there
--         is nothing left to rebind a row to anyway).
--       * the row's own binding_kind names a group or host that no longer
--         exists — this is true precisely when THAT group or host was what
--         just got deleted, and false for a direct delete of a
--         still-validly-bound row (internal/shared's own projection
--         removal, where the group/host is untouched).
--     An org-bound shared row (binding_kind = 'org') never matches either
--     branch, so it is never intercepted here — its only possible removal
--     is org_id's own cascade, already excluded by the first condition.
--     Verified against all four real cases in the same sandbox: deleting a
--     bound group rebinds only the shared row at that scope (an ordinary
--     custom check at the same group still cascades away, unchanged);
--     deleting a bound host does the same; a direct delete of an
--     already-org-bound shared row (the provider's own definition removal)
--     actually removes it; deleting the whole organization removes every
--     one of its check_definitions rows, shared or not, with zero left
--     behind.
--
-- enabled is left exactly as the customer last set it — decision 2 gives
-- enabled to the projection regardless of binding, and a customer who had
-- disabled a check before deleting its group should not have it silently
-- re-enabled by losing the binding that triggered this.
-- Two limits worth knowing before trusting this trigger:
--
--   - The UPDATE below can only land while the ('org', NULL, NULL, key)
--     slot is free, because of 00043's UNIQUE NULLS NOT DISTINCT. What
--     keeps it free is checks.Service.refuseKeyHeldBySharedProjection,
--     which refuses an organization's own check taking a key a shared
--     projection already holds at any scope. If that guard is ever
--     removed or narrowed, this UPDATE starts raising 23505 and aborting
--     the customer's own DELETE FROM monitoring_groups — the exact
--     outcome the fix exists to prevent, arriving through the fix itself.
--   - A row-level trigger does not fire on TRUNCATE. Nothing in this
--     product truncates check_definitions, so this is a note for whoever
--     one day writes something that does, not a live gap.
--
-- +goose StatementBegin
CREATE FUNCTION preserve_shared_check_definition_on_cascade() RETURNS trigger AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM organizations WHERE id = OLD.org_id)
     AND (
       (OLD.binding_kind = 'group' AND NOT EXISTS (SELECT 1 FROM monitoring_groups WHERE id = OLD.group_id))
       OR
       (OLD.binding_kind = 'host' AND NOT EXISTS (SELECT 1 FROM hosts WHERE id = OLD.host_id))
     )
  THEN
    UPDATE check_definitions
    SET binding_kind = 'org', group_id = NULL, host_id = NULL, updated_at = now()
    WHERE id = OLD.id;
    RETURN NULL;
  END IF;
  RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER check_definitions_preserve_shared_on_cascade
  BEFORE DELETE ON check_definitions
  FOR EACH ROW
  WHEN (OLD.origin = 'shared')
  EXECUTE FUNCTION preserve_shared_check_definition_on_cascade();

-- +goose Down
DROP TRIGGER check_definitions_preserve_shared_on_cascade ON check_definitions;
DROP FUNCTION preserve_shared_check_definition_on_cascade();
