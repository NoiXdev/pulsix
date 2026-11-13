# Alert Rules for Any Check Key Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Org/group/host alert rules judge every check that reports their metric, whatever its key; an item-scoped rule can name the check's key.

**Architecture:** `rules.Resolve` matches org/group/host rules on the tuple's metric alone (a metric belongs to exactly one plugin). Item-scoped rules gain `item_check_key` (new nullable column `check_rules.item_check_key`, additive migration, sqlc), which Resolve uses for the item tuple instead of the plugin's key. API DTOs and the SPA rule dialog carry it; the check form hint changes.

**Tech Stack:** Go, PostgreSQL migration + sqlc (v1.31.1 binary at `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/bin/sqlc`, run `sqlc generate` from the repo root), huma OpenAPI + `web/scripts/api-gen.sh`, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-13-rules-any-check-key-design.md`

## Global Constraints

- Field and column name `item_check_key`; empty/NULL means the plugin's key (today's behaviour). Only an `item`-scoped rule may carry it; any other scope with a non-empty value is refused with a 422-mapped invalid-input error.
- The check key format is the check definitions' (`^[a-z0-9][a-z0-9-]{0,62}$`, `internal/checks/definitions.go` `keyPattern`). `internal/rules` never imports `internal/checks`: move the pattern into the leaf `internal/checkplugins` (e.g. `checkplugins.ValidCheckKey(string) bool`) and have both packages use it — one definition, no copy.
- `check_rules.check_key` keeps the plugin's key (derived from the metric); `checkTreeBelongsTo` unchanged.
- Migration additive: `ALTER TABLE check_rules ADD COLUMN item_check_key text` plus a CHECK `(item_check_key IS NULL OR scope = 'item')`, next free number after `00086`; follow `docs/upgrades.md`'s migration rules.
- Repo rules: TDD; never `go test ./...` or `make test` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …`, plus ONE whole internal/api run at the end of the task that touches it); `golangci-lint run` clean; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never touch Docker containers other than testcontainers the tests start; no state-changing git besides your own commits.

## Review Focus

1. A rule now matching tuples it must not: another plugin's metric, another host's tuple, a group rule on a host outside the group.
2. Precedence: a host rule and an org rule over a non-default key's tuple — the deeper scope still wins; two checks' tuples never share a state or alert.
3. An item rule with `item_check_key` naming a key the host does not have: an Effective with Observed false, no panic, no alert.
4. Old rows (NULL column) behave exactly as before; a rule updated without the field keeps or clears it as the API documents.
5. The SPA never sends `item_check_key` on a non-item scope (switching scope after choosing a check clears it).

---

### Task 1: Matching, column, service, API

**Files:** `internal/rules/resolve.go`, `rule.go`, `service.go`, `store_sql.go` (+ tests: `resolve_test.go`, service tests, a DB-backed evaluation test); `internal/db/migrations/000NN_rule_item_check_key.sql`; `internal/db/queries/rules.sql` + `internal/db/gen` (sqlc); `internal/checkplugins` (key format + a test that no two descriptors share a metric name) and `internal/checks/definitions.go` using it; `internal/api/rule_handlers.go` (+ apitest tests); `web/src/api/schema.d.ts` (api-gen).

**Produces:** `Rule.ItemCheckKey string`, `RuleInput.ItemCheckKey string`; DTO field `item_check_key` (`json:"item_check_key,omitempty"`) on the rule input and output DTOs; `checkplugins.ValidCheckKey`.

- [ ] Tests first: Resolve table (org/group/host rule × tuples under `nginx`, `nginx-internal`, `nginx-2` and an `apache` tuple; item rule with and without `item_check_key`; Observed false for a missing key; precedence host-over-org on a non-default key); service (refused on non-item scope, refused on bad format, trimmed/stored/read back, update semantics as documented in the DTO doc string); store round-trip and the CHECK; an evaluation test with two keys of one plugin on one host and one org rule → two independent states; apitest create/read with `item_check_key` and the effective rules listing a non-default key's tuple. RED → GREEN → sqlc generate (no other diff) → api-gen → lint → tests (rules, checks, checkplugins, api `-run`, then the whole internal/api once) → commit `feat(rules): judge every check of a metric's plugin, whatever its key`.

### Task 2: SPA

**Files:** `web/src/features/checks/RuleDialog.tsx`, `RulePickers.tsx`, `RuleRow.tsx` (or wherever the list shows a rule's item), i18n en/de (`checks.ruleDialog.*` new labels, `checks.rulesKey` new text), Vitest.

- [ ] Tests first: item scope shows a "Check" select with the host's checks running the metric's plugin (data from the host checks hook the SPA already has), preselected to the plugin's key when present; the item picker lists that check's items; the payload carries `item_check_key` (state in the report whether it is always sent for item scope or only when it differs from the plugin's key); switching away from item scope drops it; edit prefill round-trips; the rules list shows `item (key)` when the key is not the plugin's; the check form hint text. RED → GREEN → `npm test`, `npm run lint`, `npx tsc -b` → commit `feat(web): pick the check of an item rule and say rules apply to every key`.

### Task 3: Documentation

**Files:** `docs/BACKLOG.md` (3.13e done; the 3.11c leftover row note may now mention that sqlc is available for its range filter — leave 3.11c's text as is), `docs/upgrades.md` (behaviour change: existing rules incl. seeded ones start judging non-default keys; the migration); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-rules-any-key` from `docs/pulsix-finite-values`: the rules page, the checks page's key note, changelog. `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record rules for every check key (backlog 3.13e)`, `docs(pulsix): rules apply to every check of a type`).
