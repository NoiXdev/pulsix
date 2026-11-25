# A tuple back from unmeasured starts fresh, smart reports a sleeping disk (backlog 4.42, 3.9b) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 4.42 and 3.9b (`grep -nE "^\| (4\.42|3\.9b) " docs/BACKLOG.md`).

**Architecture:** 4.42 is in `internal/rules` (`evaluate.go`: the unmeasured skip, `decide`'s use of `Previous`) and the evaluation store that persists `check_states` (`internal/rules/*_sql.go`, `internal/db/queries`). 3.9b is in the agent's smart plugin (`internal/agent/checks/plugins/smart`) and its manager descriptor (`internal/checkplugins/smart.go`), reusing 3.74's `MetricAskedForItem` hook (`internal/checkplugins/descriptor.go`).

**Tech Stack:** Go, sqlc, Postgres, React/TypeScript only for metric texts.

**Spec:** the backlog rows plus the owner's decisions of 2026-11-25 (below). Background: 3.74's plan `docs/superpowers/plans/2026-11-25-snmp-retry-zfs-exported-3.70-3.74.md` (the hook) and 4.39's (`continues_alert_id`, adoption within one hour of a stale close).

## Owner decisions (2026-11-25, binding)
- **4.42:** start fresh. A tuple that was not measured for a while (check disabled or removed, host un-monitored, selector edit, a metric not asked) starts again from zero when it is measured again: its new state's `since` is the first new judgement, so a new alert opens at that moment and a rule's "for N minutes" duration counts anew.
- **3.9b:** standby-aware state (not waking the disk): the smart check reports that a disk sleeps, and while it sleeps its other readings are not asked, so the host does not turn unknown and the disk is not forgotten.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; SPA `cd web && npm run lint && npx vitest run <files> && npx tsc -b` if touched; regenerate the API client the repo's way if the OpenAPI changes.
- Migrations additive only, next free number (`ls internal/db/migrations | tail -2`); a new column: run `./internal/backup/...`.
- Timestamps compared in tests are truncated to microseconds.
- TDD: the failing test first, shown failing for the right reason. Deterministic tests.
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **4.42 mechanism.** The `check_states` row of an unmeasured tuple keeps standing (the sweep and 4.39 rely on that — read the long comment at the unmeasured skip in `rules.Evaluate`), but the store records that it went unmeasured: an additive nullable column (e.g. `check_states.unmeasured_at timestamptz`) set by the evaluation's write path when the tuple is reported in `Result.Unmeasured` (only when not already set), and cleared whenever the tuple's state is written again. The evaluation's `Previous` read treats a row with that mark as **no previous state** for `decide`: fresh `since`, no carried candidate/pending duration, no flap history carried from before (check what `Previous` feeds — flapping counts, last change — and say in the report what restarts). The same applies to a tuple skipped because its item is ignored by a decision, if that path also keeps the row (check; if the ignore path deletes the row via `Dropped`, nothing to do there). A client host's frozen period (3.57) is **not** unmeasured and keeps today's behaviour. The `Unmeasured` report itself is unchanged (4.39 depends on it). Tests in `internal/rules` (pure `Evaluate` with a `Previous` carrying the mark → fresh since) and through the SQL store (disable a check, evaluate, re-enable, evaluate: the state's since is the re-enabled evaluation's judgement time, and the alert that opens has `opened_at` after the previous episode's `closed_at`; with 4.39 it still adopts the acknowledgement). Upgrade note: existing rows have no mark, so a tuple unmeasured at the upgrade still resumes its old since once (say so).
- **3.9b agent.** The smart plugin reports a new discrete metric `smart.power_mode` per device item on every round that queried the device: `standby` when smartctl skipped the disk because it sleeps (interpret's existing standby detection: exit 0, JSON without a `smart_support` block — use `testdata/standby.json`), `active` otherwise when the device reported readings. A device without SMART data (virtual disk, USB bridge) reports no `smart.power_mode` (unchanged behaviour). A standby round must NOT make `checks.VanishedMetrics` flag the device's remembered metrics as "no longer reported": the standby item reports a label, which today would count as "the item reports a value" — exclude the standby round from the vanished bookkeeping for that item (do not refresh its `lastValue` with the power-mode label either, or the 24 h memory would be extended forever — decide and justify; the requirement is that waking up after any sleep length reports the readings normally and nothing is flagged as vanished meanwhile). `smart.devices` counts sleeping devices as today (check what it does now and keep it). Package doc updated. Tests with the recorded fixtures.
- **3.9b manager.** `internal/checkplugins/smart.go` gains `{Name: "smart.power_mode", Kind: Discrete}` and a `MetricAskedForItem` hook: while the item's current `smart.power_mode` is `standby`, no other smart metric of that item is asked (`smart.power_mode` itself always is; the host item `""` and `smart.devices` unaffected). Share the label constants the way 3.74 did for zfs (`checkplugins` exports them, the agent plugin uses them). Rules catalogue test, descriptor tests, an evaluation-level test like 3.74's (`internal/rules/load_item_state_sql_test.go`): a sleeping disk with an old `smart.temperature_celsius` reading leaves the host OK; once `active` again, a stale reading counts as stale. SPA metric texts (en, de) if the catalogue has per-metric texts. Upgrade note: an older agent never reports `smart.power_mode`, so its sleeping disks behave as before; the docs' advice to list only always-on disks can go for updated agents.

---

## Task 1: 4.42 a tuple back from unmeasured starts fresh
**Files:** `internal/rules/{evaluate.go,…_sql.go}` (+ tests), `internal/db/queries/*.sql` (+ sqlc), migration, `docs/upgrades.md`.
- [ ] Commit `fix(rules): a tuple measured again after a gap starts a fresh state`.

## Task 2: 3.9b smart reports a sleeping disk
**Files:** `internal/agent/checks/plugins/smart/*`, `internal/agent/checks/vanished.go` if needed (+ tests), `internal/checkplugins/smart.go` (+ tests), rules catalogue test, SPA metric texts, `docs/upgrades.md`.
- [ ] Commit `feat(smart): a sleeping disk reports standby and its readings wait`.

## Task 3: Final
Whole-branch review, `make test`, lint, `docs/upgrades.md`, user docs in the docs clone (smart standby, rule durations after re-enabling, changelog), backlog rows done, merge to local main, redeploy.
