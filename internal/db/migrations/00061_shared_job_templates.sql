-- +goose Up
-- shared_job_templates is a job template the INSTALLATION owns, projected
-- into every organization as an ordinary job_templates row — the second
-- application of the mechanism migration 00060 built for repositories
-- (spec v0.2f decisions 1, 2). No org_id, no RLS, no policy pair: the same
-- instance_settings posture shared_repos has had since 00060 (itself since
-- 00009).
--
-- shared_repo_id is NOT NULL on purpose, even though spec decision 2's own
-- table lists only "playbook path" on the definition's side: a path alone
-- names nothing without the repository it is relative to, and the
-- resolution the text right under that table describes ("each one syncs
-- its own clone of the repository and gets its own playbooks row") only
-- works when every organization is guaranteed to have a clone under the
-- SAME path — which holds only for a repository that is itself a shared
-- definition (migration 00060), never an organization's own. A shared
-- template therefore always names which shared repository its playbook
-- lives in. ON DELETE RESTRICT: a repository definition still named by a
-- template definition cannot be removed out from under it; the service
-- reports that before the database has to.
CREATE TABLE shared_job_templates (
  id                uuid PRIMARY KEY,
  name              text NOT NULL UNIQUE,
  shared_repo_id    uuid NOT NULL REFERENCES shared_repos (id) ON DELETE RESTRICT,
  playbook_path     text NOT NULL,
  "limit"           text NOT NULL DEFAULT '',
  extra_vars        jsonb NOT NULL DEFAULT '{}',
  extra_vars_schema jsonb,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

-- shared_template_id non-null means this job_templates row is a projection
-- (git_repos.shared_repo_id's own comment, migration 00060, applies here
-- unchanged: single-column because the parent has no org_id to compose
-- with, ON DELETE RESTRICT because spec decision 5 is reported as ErrInUse
-- before the database ever has to refuse).
ALTER TABLE job_templates
  ADD COLUMN shared_template_id uuid REFERENCES shared_job_templates (id) ON DELETE RESTRICT;

-- playbook_id becomes nullable for exactly one reason: a projection is
-- written into every organization immediately when its definition is
-- created (spec decision 4), including one whose own clone of the shared
-- repository has not synced the definition's playbook_path yet — there may
-- be no playbooks row to resolve against at that moment. This only loosens
-- what a PROJECTION needs; an ordinary, non-shared template's playbook_id
-- is still always resolved before internal/templates/service.go ever
-- writes one (its own playbooks.Get lookup is unchanged and still
-- required), so nothing an existing template relies on today changes.
ALTER TABLE job_templates ALTER COLUMN playbook_id DROP NOT NULL;

-- +goose Down
ALTER TABLE job_templates ALTER COLUMN playbook_id SET NOT NULL;
ALTER TABLE job_templates DROP COLUMN shared_template_id;
DROP TABLE shared_job_templates;
