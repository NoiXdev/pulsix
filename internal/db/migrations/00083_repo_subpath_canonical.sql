-- +goose Up
-- A repository subpath in one stored form (backlog 7.94). The repos and
-- shared services now store what repos.CanonicalSubpath returns: a
-- clone-relative path without empty or "." segments, "" for the
-- repository root. Before, they stored what was sent, and the worker
-- refuses a run whose playbook path starts with "/" and cannot open
-- "./ansible" or "a//b" for a sync. This rewrites the values stored so far
-- the same way, so those repositories work without being saved again.
--
-- Definitions and their projections get the same rewrite, so a
-- projection stays equal to its definition. Only empty and "." segments
-- go: a value with ".." (which the shared service never refused) stays as
-- it was; the worker refuses it as before, and the next save of it is
-- refused. The filter keeps rows already in that form untouched. The
-- migration runs with no organization bound, so git_repos' bypass policy
-- lets it see every organization.
UPDATE shared_repos s
SET subpath = COALESCE((
  SELECT string_agg(seg, '/' ORDER BY ord)
  FROM unnest(string_to_array(s.subpath, '/')) WITH ORDINALITY AS t(seg, ord)
  WHERE seg NOT IN ('', '.')), '')
WHERE s.subpath <> '' AND s.subpath ~ '(^|/)\.?(/|$)';

UPDATE git_repos r
SET subpath = COALESCE((
  SELECT string_agg(seg, '/' ORDER BY ord)
  FROM unnest(string_to_array(r.subpath, '/')) WITH ORDINALITY AS t(seg, ord)
  WHERE seg NOT IN ('', '.')), '')
WHERE r.subpath <> '' AND r.subpath ~ '(^|/)\.?(/|$)';

-- +goose Down
-- Nothing to undo: the old spellings named the same directories, and the
-- services before this migration read the rewritten values as they are.
SELECT 1;
