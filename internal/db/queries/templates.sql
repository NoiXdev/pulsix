-- name: GetJobTemplateForRun :one
-- The one template read the enqueue path needs in Task 5: the template row
-- joined with its playbook, so the claim can build the worker payload
-- (inline YAML or "repo:<repo-id>:<path>") without a templates service —
-- that service arrives with Task 7 and shares this table. Scoped by org_id
-- in the WHERE clause, not only relying on RLS (see GetZone's comment in
-- hosts.sql for the reasoning).
SELECT t.id, t.org_id, t.playbook_id, t.scope, t."limit", t.extra_vars,
       t.extra_vars_schema, t.credential_id,
       p.source AS playbook_source, p.repo_id AS playbook_repo_id,
       p.path AS playbook_path, p.inline AS playbook_inline
FROM job_templates t
JOIN playbooks p ON p.org_id = t.org_id AND p.id = t.playbook_id
WHERE t.org_id = $1 AND t.id = $2;
