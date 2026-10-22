# Migrations

Numbered goose migrations, applied by `internal/db`'s `goose.UpContext` at
manager start under an advisory lock. Only `*.sql` here is embedded, so
this file is documentation and nothing else.

## 00030 is permanently retired

There is no `00030_*.sql` and there must never be one. `goose.UpContext`
runs **without** `WithAllowMissing`: goose compares each file against the
highest version already recorded in `goose_db_version`, and a file whose
number is below that high-water mark is an error, not something it applies
late. Every database that has ever reached 00031 or beyond is already past
00030, so adding a migration under that number now would refuse to start
the manager on exactly the installations that are furthest along —
upgraded ones — while a fresh install would accept it. Pick the next free
number after the highest file in this directory.

## Rules

- Additive only. Never drop or rewrite a column a shipped release reads;
  retire it in a later release once nothing reads it (the pattern backlog
  2.93 follows for `git_repos.vault_credential_id`).
- Every org-owned table gets the RLS policy pair — `org_isolation` and
  `org_isolation_bypass` — and both `ENABLE` and `FORCE ROW LEVEL
  SECURITY`. `FORCE` is what makes the policies apply to the table's owner
  too, which is the role the manager connects as.
- A child of an org-owned table references its parent on the composite
  `(org_id, id)`, not on `id` alone: that is the second tenancy mechanism
  beside RLS, and it is the one that still holds when a query runs under
  the bypass policy.
- A constraint added to an existing table validates every existing row, so
  a migration that adds one must first repair the rows that would fail it
  — otherwise it aborts the upgrade on precisely the installations that
  need it (see 00065, which nulls orphaned `worker_id` values before
  adding the foreign key).
