# Job chaining (backlog 2.121) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A job template, or one manual run of it, names follow-up templates; when the whole run succeeds, Pulsix starts each follow-up exactly once on its own scope narrowed to the hosts the run played on.

**Architecture:** Follow-ups configured on a template live in `job_template_follow_ups`. Every launch snapshots its follow-ups into `run_follow_ups` (`pending`) *before* its jobs are created. A new run-group sink marks them `due` (with the hosts the run played on) or `skipped` inside the finishing transaction. A ticker in every manager (`chain.Starter`) sweeps rows the sink missed and starts `due` rows through `jobs.Service.EnqueueRunWith` with `Trigger: chain` and an idempotency key per row. The run detail reads the same table.

**Tech Stack:** Go 1.x, PostgreSQL (RLS, sqlc in `internal/db`), huma OpenAPI, React + TanStack Query + openapi-fetch, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-16-job-chaining-design.md` — read it first; this plan does not repeat its reasoning.

## Global Constraints

- Layers per `CLAUDE.md`: handlers decode/call/encode only; services hold the rules and depend on interfaces declared next to them; SQL lives in stores (sqlc `internal/db/queries/*.sql` → `internal/db/gen`). No package-level state; time injected.
- Migrations additive only (`docs/upgrades.md`). One new migration; number = next free at implementation time (`main` is at 00098; check `ls internal/db/migrations` and any open branch before naming it).
- Condition values: only `success`. Follow-up states: `pending`, `due`, `started`, `skipped`, `dismissed`.
- Limits: at most **10** follow-ups per template and per run override; chain depth at most **10**; start attempts before giving up **5**; sweep age **1 minute**; starter tick default **5 s**.
- Idempotency key of a chained start: `chain:<run_follow_ups.id>`.
- Audit: background action `chain.run`, actor kind `chain`, actor id = parent run group id, object type `job_template`, object id = follow-up template.
- Exact user-visible reason strings (English, stored in `run_follow_ups.reason`; the SPA shows them as sent):
  - `the run failed` · `the run was cancelled` · `the run did not start` · `the run played on no host`
  - `the template was deleted` · `the chain is too long` · `the template is already in this chain`
  - `none of the hosts is in this template's scope`
  - `every host is in a maintenance window` · `<n> host(s) in a maintenance window` (guard `skip`) · `<n> host(s) left out for maintenance` (guard `exclude_hosts`, written on a started row)
  - `waiting for the active run of <template name>` (row stays `due`)
  - `could not start: <error>` (after 5 attempts)
  - `dismissed by <user name>`
- Cycle message (422): `follow-ups would form a cycle: a → b → a` (template names).
- Delete refusal (409): `this template is a follow-up of: <names, comma-separated>`.
- Public docs name no tech stack. Code, comments, commits in English; Conventional Commits with footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Before every commit: the task's tests, `golangci-lint run` on touched packages, and for SPA tasks `npm run lint && npm run test -- <files>` in `web/`. Race suite as `make test` only at the end (Task 13), never one whole-module `go test ./...`.

## Review Focus

1. **A run narrowed by a limit or the playbook's `hosts:`** keeps every zone host in `jobs.host_ids`; the follow-up must still run only on hosts in the recap. → integration test `TestPlayedHostsIgnoresTargetedButUnplayed` (Task 4).
2. **Two managers ticking at once** must not start one follow-up twice. → `TestStarterTwoStartersStartOnce` (Task 5).
3. **A follow-up template deleted, renamed or re-scoped while its row is `due`** → skipped with a reason or started on the new scope; never a 500 loop. → `TestStarterDeletedTemplate`, `TestStarterNoHostsInScope` (Task 5).
4. **A follow-up whose template is running when it becomes due** waits and starts after; it does not give up after 5 ticks. → `TestStarterWaitsForActiveRunWithoutCountingAttempts` (Task 5).
5. **A launch that creates no job** (empty scope, every host excluded, enqueue error) must not leave `pending` rows forever. → `TestSweepResolvesGroupWithoutJobs` (Task 4).

---

### Task 1: Migration and queries

**Files:**
- Create: `internal/db/migrations/000NN_job_chaining.sql`
- Create: `internal/db/queries/chain.sql`
- Modify: `internal/db/queries/templates.sql` (follow-up edges)
- Regenerate: `internal/db/gen/*` (`make sqlc` or `sqlc generate`, as the Makefile names it)
- Test: `internal/db/migrations_chain_test.go` (or the package the other migration tests live in — follow `internal/db/testdb` usage there)

**Interfaces:**
- Produces tables `job_template_follow_ups(org_id, template_id, follow_up_template_id, condition, position)` and `run_follow_ups(id, org_id, parent_group_id, template_id, condition, position, state, reason, child_group_id, host_ids, root_trigger, root_ref, depth, path, configured_by, attempts, next_attempt_at, created_at, decided_at, started_at)`, both with RLS policies copied from `job_template_vault_credentials` (migration 00059) and an `org_id` composite FK to `job_templates` where the spec says FK.
- `jobs.trigger` CHECK widened to `manual, schedule, webhook, system, chain` (drop and re-add the named constraint from 00024 — widening is additive).
- Indexes: `run_follow_ups (parent_group_id, template_id)` UNIQUE; partial `(next_attempt_at) WHERE state = 'due'`; partial `(created_at) WHERE state = 'pending'`; `(child_group_id)`.
- sqlc queries (names fixed; later tasks call them):
  - templates.sql: `ListTemplateFollowUps(org, template)`, `ListOrgFollowUpEdges(org)` → `(template_id, follow_up_template_id)`, `DeleteTemplateFollowUps(org, template)`, `InsertTemplateFollowUp(org, template, follow_up, position)`, `ListFollowUpReferrers(org, follow_up_template_id)` → referrer names.
  - chain.sql: `PlanTemplateFollowUps` (INSERT … SELECT from `job_template_follow_ups` ON CONFLICT DO NOTHING), `InsertRunFollowUp` (ON CONFLICT DO NOTHING), `CopyRunFollowUps(from_group, to_group, configured_by)`, `ListRunFollowUps(org, parent_group)`, `GetFollowUpByChild(org, child_group)`, `ResolvePendingFollowUps(org, parent_group, state, reason, host_ids, now)`, `PlayedHosts(org, group)`, `LockDueFollowUps(now, lim)` (raw pool, bypass policy, `FOR NO KEY UPDATE SKIP LOCKED`, like `LockDueSchedules`), `ListStalePendingFollowUps(before, lim)` (pending, older than `before`, and no non-terminal `kind='run'` job in the parent group), `RunGroupOutcome(org, group)` → (jobs, failed_or_error, cancelled), `MarkFollowUpStarted`, `MarkFollowUpSkipped`, `MarkFollowUpWaiting(reason, next_attempt_at)`, `BumpFollowUpAttempt(error, next_attempt_at)`, `DismissPendingFollowUps(org, parent_group, reason, now)`.

`PlayedHosts` rule (the one algorithm this task fixes): for each `kind='run'` job of the group, the elements of `ok_hosts ∪ changed_hosts ∪ skipped_hosts` that pass `pg_input_is_valid(h,'uuid')`, are `= ANY(host_ids)`, and are not in that job's `failed_hosts ∪ unreachable_hosts`; DISTINCT, ordered. Use the `jsonb_typeof(...) = 'array'` guard from `runGroupQuery` (`internal/jobs/run_group.go`).

- [ ] **Step 1: Write failing tests** `TestJobChainingMigration`: inserting a follow-up edge with `template_id = follow_up_template_id` fails the CHECK; deleting a template that is a `follow_up_template_id` fails with FK violation; deleting the owning template cascades its edges; a `jobs` row with `trigger='chain'` inserts; RLS hides another org's `run_follow_ups` rows; duplicate `(parent_group_id, template_id)` is a no-op through `InsertRunFollowUp`.
- [ ] **Step 2: Run** `go test ./internal/db/... -run TestJobChainingMigration -timeout 900s` — FAIL (tables missing).
- [ ] **Step 3: Write the migration, the queries, regenerate gen.**
- [ ] **Step 4: Run the test again** — PASS. Also `TestPlayedHosts` table test here against hand-built `jobs.stats`: host_ids {h1,h2}, ok [h1], skipped [] → [h1]; ok [h1,h2], failed [h2] → [h1]; ok ["localhost", h1] → [h1]; stats `{"ok_hosts": null}` → [].
- [ ] **Step 5: Commit** `feat(db): tables for job chaining`

### Task 2: Follow-ups on templates

**Files:**
- Create: `internal/templates/follow_ups.go` (validation + cycle detection), `internal/templates/follow_ups_test.go`
- Modify: `internal/templates/service.go` (Template, CreateInput, UpdateInput, Create, Update, Get, List, Delete), `internal/templates/store_sql.go`, `internal/templates/store_sql_test.go`
- Modify: `internal/shared/store_sql.go` (`DeleteTemplateWithProjections`: explicit follow-up check → `&InUseError{Reasons: []string{"a follow-up"}}`, like its job-trigger check), its test

**Interfaces:**
- `type FollowUp struct { TemplateID uuid.UUID; Name string }` — `Template.FollowUps []FollowUp` (ordered).
- `CreateInput.FollowUps []uuid.UUID`; `UpdateInput.FollowUps *[]uuid.UUID` (nil keeps the stored list, empty slice clears).
- Store additions: `ReplaceFollowUps(ctx, orgID, templateID uuid.UUID, ids []uuid.UUID) error`, `ListFollowUps(ctx, orgID, templateID uuid.UUID) ([]FollowUp, error)`, `ListOrgFollowUps(ctx, orgID uuid.UUID) (map[uuid.UUID][]FollowUp, error)`, `LockFollowUpGraph(ctx, orgID uuid.UUID) error` (transaction advisory lock keyed on the org, taken inside `InTx`, pattern of `WithTreeLock`), `FollowUpReferrers(ctx, orgID, templateID uuid.UUID) ([]string, error)`.
- Errors: `ErrFollowUpCycle` (wraps `ErrInvalidInput`, message is the spec's cycle text), `type InUseError struct{ Referrers []string }` returned by `Delete` (handler maps to 409).
- `func findCycle(edges map[uuid.UUID][]uuid.UUID, from uuid.UUID) []uuid.UUID` — returns the cycle path starting and ending at `from`, nil when none (DFS).
- `Service.Update`/`Create` validate follow-ups inside `InTx` after `LockFollowUpGraph`: each exists in the org (else `ErrNotFound`-wrapped "follow-up template does not exist"), none equals the template, no duplicates, at most 10, then `findCycle` over `ListOrgFollowUps` with this template's edges replaced. Projections may set follow-ups (not in `refuseSharedFieldChange`).
- Revision: `ReplaceFollowUps` runs in the same tx as `Store.Update`, so the bumped revision covers it. On Create the list is written after the row.

- [ ] **Step 1: Failing unit tests** in `follow_ups_test.go`: `TestFindCycle` table (none; direct a→b→a gives [a b a]; indirect a→b→c→a; diamond a→b, a→c, b→d, c→d has none); `TestUpdateRefusesCycle` (fake store: b→a stored, update a with [b] → `ErrFollowUpCycle`, message `follow-ups would form a cycle: a → b → a`); `TestUpdateRefusesSelfDuplicateAndEleven`; `TestUpdateNilFollowUpsKeepsList`; `TestDeleteReferencedTemplate` (→ `*InUseError{Referrers: ["cloudpanel"]}`).
- [ ] **Step 2: Run** `go test ./internal/templates -run 'FindCycle|FollowUp|DeleteReferenced' -timeout 900s` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Store integration tests** in `store_sql_test.go`: round trip with order; `TestConcurrentSavesCannotCloseACycle` — two goroutines through a start barrier save a→[b] and b→[a] many times; afterwards the stored graph has no cycle and exactly one save of each pair failed with `ErrFollowUpCycle` (pattern: the 2.15 concurrency tests). Shared: deleting a shared definition whose projection is a follow-up → `InUseError` with "a follow-up".
- [ ] **Step 5: Run** `go test ./internal/templates ./internal/shared -timeout 900s` — PASS.
- [ ] **Step 6: Commit** `feat(templates): follow-up templates with cycle refusal`

### Task 3: Jobs — chain trigger, host narrowing, planning port, fan-out sink

**Files:**
- Create: `internal/jobs/follow_ups.go`, `internal/jobs/follow_ups_test.go`
- Modify: `internal/jobs/run_request.go` (`RunRequest`, `validate`, `EnqueueRunWith`), `internal/jobs/service.go` (`resolveRunTargets` gains the `OnlyHosts` filter next to `onlyHost`; `RetryFailed`), `internal/jobs/run_group.go` (fan-out)
- Modify: `internal/app/template_lookup.go` only if `TemplateRow` must carry the name (the starter's waiting reason needs it — add `Name string` to `TemplateRow` if absent)

**Interfaces:**
- `const TriggerChain = "chain"`; `const MaxFollowUps = 10`.
- `type ChainContext struct { RootTrigger string; RootRef *uuid.UUID; Depth int; Path []uuid.UUID; ConfiguredBy *uuid.UUID }`
- `RunRequest` gains `OnlyHosts []uuid.UUID` (intersect resolved targets; empty result → `ErrHostNotInScope`), `FollowUps *[]uuid.UUID` (manual only), `Chain *ChainContext` (chain only).
- `validate`: `chain` requires `TriggerRef` and `Chain`; `FollowUps != nil` only with `TriggerManual`; `Chain != nil` only with `TriggerChain`.
- `type FollowUpPlanner interface { PlanFollowUps(ctx context.Context, orgID, groupID, templateID uuid.UUID, override *[]uuid.UUID, c ChainContext) error; CopyFollowUps(ctx context.Context, orgID, fromGroupID, toGroupID uuid.UUID, configuredBy *uuid.UUID) error }`; `func (s *Service) SetFollowUpPlanner(p FollowUpPlanner)`. Unwired → no planning (tests and old callers unchanged).
- `EnqueueRunWith`: after `validate` and the template lookup, before `enqueueRunScope`, validate an override (each id via `s.templates.TemplateLookup`, `ErrNotFound` → `ErrInvalidInput` "follow-up template %s does not exist"; not the template itself; no duplicates; ≤ 10) and call `PlanFollowUps` with `ChainContext` = `req.Chain` with `Depth+1` and `Path+[templateID]`, or for a non-chain run `{RootTrigger: req.Trigger, RootRef: req.TriggerRef, Depth: 1, Path: [templateID], ConfiguredBy: req.Actor}`. Planning runs before the jobs exist on purpose (no finish-before-plan race; the sweep resolves a group that never got jobs). A planner error fails the enqueue.
- `RetryFailed`: compute the new group id first, call `CopyFollowUps(org, j.RunGroupID, newGroup, actor)`, then `enqueueRunScope` with that group.
- `type RunGroupSinks []RunGroupSink` with `RunGroupFinished` calling every sink, returning `errors.Join` of their errors (each sink keeps its own savepoint, so one failing does not undo another).

- [ ] **Step 1: Failing tests** (fakes as in `run_request_test.go`): `TestValidateChainTrigger` table; `TestOnlyHostsIntersectsZones` (two zones {h1,h2},{h3}; OnlyHosts {h2,h3,h9} → zones {h2},{h3}); `TestOnlyHostsNoneLeft` → `ErrHostNotInScope`; `TestEnqueuePlansBeforeJobs` (fake planner records the call, fake store sees no job yet at that moment; context for a manual run is depth 1, path [tpl], root manual, configured_by = actor); `TestEnqueueChainContextExtends` (req.Chain depth 2 path [a] → planner gets depth 3 path [a, b]); `TestEnqueueRejectsBadOverride` (self, duplicate, 11 ids, unknown id → `ErrInvalidInput`); `TestRetryCopiesFollowUps` (planner `CopyFollowUps` called with the original group and the retry group, before job creation); `TestRunGroupSinksCallsAllAndJoinsErrors`.
- [ ] **Step 2: Run** `go test ./internal/jobs -run 'Chain|OnlyHosts|Plans|Override|RetryCopies|RunGroupSinks' -timeout 900s` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same — PASS; then `go test ./internal/jobs -timeout 900s` — PASS (existing behaviour unchanged with no planner wired).
- [ ] **Step 5: Commit** `feat(jobs): chain trigger, host narrowing and follow-up planning port`

### Task 4: `internal/chain` — store, planner, sink, sweep

**Files:**
- Create: `internal/chain/followup.go` (types), `internal/chain/store_sql.go`, `internal/chain/store_sql_test.go`, `internal/chain/sink.go`, `internal/chain/sink_test.go`

**Interfaces:**
- `type State string` with `StatePending, StateDue, StateStarted, StateSkipped, StateDismissed`.
- `type FollowUp struct { ID, OrgID, ParentGroupID, TemplateID uuid.UUID; TemplateName string; Position int; State State; Reason string; ChildGroupID *uuid.UUID; ChildFirstJobID *uuid.UUID; HostIDs []uuid.UUID; RootTrigger string; RootRef *uuid.UUID; Depth int; Path []uuid.UUID; ConfiguredBy *uuid.UUID; Attempts int; NextAttemptAt *time.Time; CreatedAt time.Time }`
- `SQLStore` (`NewSQLStore(d *db.DB) *SQLStore`) implements `jobs.FollowUpPlanner` plus:
  - `ResolveGroup(ctx context.Context, orgID, groupID uuid.UUID, status jobs.Status, now time.Time) error` — inside the caller's transaction: succeeded → `PlayedHosts`, empty → skipped `the run played on no host`, else `due` with those hosts and `next_attempt_at = now`; failed/error → skipped `the run failed`; cancelled → skipped `the run was cancelled`.
  - `SweepStale(ctx context.Context, now time.Time, limit int) (int, error)` — raw pool tx; for each `ListStalePendingFollowUps(now-1m)` group: `RunGroupOutcome` 0 jobs → skipped `the run did not start`, else derive status like `RunGroupResult.Status` and `ResolveGroup`.
  - `ListForGroup(ctx, orgID, groupID uuid.UUID) ([]FollowUp, error)`, `ParentOf(ctx, orgID, childGroupID uuid.UUID) (*FollowUp, error)` (nil when not a chained run), `Dismiss(ctx, orgID, groupID uuid.UUID, reason string, now time.Time) (int, error)`.
- `Sink` (`NewSink(store *SQLStore, now func() time.Time, log *slog.Logger) *Sink`) implements `jobs.RunGroupSink`: opens a savepoint on the ctx transaction (as `notify.Outbox.RunGroupFinished` does), calls `ResolveGroup`, ignores groups with no follow-up rows cheaply.

- [ ] **Step 1: Failing integration tests** (`testdb`): `TestPlanTemplateFollowUps` (template with [b, c] → two pending rows, positions 0,1, path/depth/root stored; second call no duplicates); `TestPlanOverride` (override [c] → one row c); `TestCopyFollowUps` (copies template/position/root/path/depth, sets configured_by, state pending, regardless of the source rows' state); `TestSinkSucceededMarksDue`; `TestPlayedHostsIgnoresTargetedButUnplayed` (job host_ids {h1,h2}, stats ok [h1] → row host_ids [h1]); `TestSinkFailedAndCancelledSkip` (reason strings exact); `TestSinkPlayedOnNoHost`; `TestSweepResolvesGroupWithoutJobs`; `TestSweepIgnoresActiveGroupAndFreshRows`; `TestDismiss` (pending only; started rows untouched).
- [ ] **Step 2: Run** `go test ./internal/chain -timeout 900s` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(chain): follow-up rows, run-group sink and sweep`

### Task 5: `internal/chain` — the starter

**Files:**
- Create: `internal/chain/starter.go`, `internal/chain/starter_test.go` (fakes), `internal/chain/starter_sql_test.go` (two starters against `testdb`)

**Interfaces (declared in `starter.go`):**
- `type Runs interface { EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error); HasActiveRun(ctx context.Context, orgID, templateID uuid.UUID) (bool, error) }`
- `type TemplateNames interface { TemplateName(ctx context.Context, orgID, id uuid.UUID) (string, error) }` (for the waiting reason; `jobs.ErrNotFound` when deleted)
- `type Maintenance interface { HostsInMaintenance(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID, at time.Time) (map[uuid.UUID]bool, error) }`
- `type ScheduleGuards interface { MaintenanceMode(ctx context.Context, orgID, scheduleID uuid.UUID) (mode string, found bool, err error) }` — adapter in `internal/app/chain_ports.go` over `schedules.Service.Get` (`Guards.Maintenance`, `""` read as `ignore`).
- `type Auditor interface { Record(ctx context.Context, e audit.Event) error }`
- `type Notifier interface { FollowUpDecision(ctx context.Context, tx pgx.Tx, d notify.FollowUpDecision) error }` (Task 6 defines `notify.FollowUpDecision`; until then the starter takes it as nil-able and skips notifying).
- `func NewStarter(store *SQLStore, runs Runs, names TemplateNames, maint Maintenance, guards ScheduleGuards, audit Auditor, now func() time.Time, log *slog.Logger) *Starter`; `func (s *Starter) SetNotifier(n Notifier)`; `func (s *Starter) Run(ctx context.Context)` (loop, tick from `NewStarter`'s option or `DefaultTick = 5 * time.Second`); `func (s *Starter) Tick(ctx context.Context) error` (one sweep + one batch of up to 50 rows; exported for tests).

Per-row order (spec "The starter"): depth ≥ 10 → skipped `the chain is too long`; template on `path` → skipped `the template is already in this chain`; `TemplateName` not found → skipped `the template was deleted`; `HasActiveRun` → stay due, reason `waiting for the active run of <name>`, `next_attempt_at = now + tick`, **attempts unchanged**; maintenance when `root_trigger = schedule` and the schedule is found (skip: any host in window → skipped `<n> host(s) in a maintenance window`; exclude_hosts: drop them, all → skipped `every host is in a maintenance window`, else start with `ExcludeHosts` and reason `<n> host(s) left out for maintenance`; unknown mode → skipped `could not start: the schedule's maintenance guard is invalid`); then `EnqueueRunWith(org, template, RunRequest{Trigger: chain, TriggerRef: &parent, OnlyHosts: row.HostIDs, ExcludeHosts: …, Chain: &ChainContext{root, depth, path, configured_by}, IdempotencyKey: "chain:"+row.ID})`; `ErrEmptyScope`/`ErrHostNotInScope`/`ErrAllHostsExcluded` → skipped `none of the hosts is in this template's scope`; `ErrNotFound` → skipped `the template was deleted`; other error → attempts+1, `next_attempt_at = now + 2^attempts × tick` (cap 5 min), reason `could not start: <err>`, at 5 → skipped with that reason; success → started with child group = `jobs[0].RunGroupID`. Every final outcome (started or skipped, not waiting) records `chain.run` with meta `{parent_group_id, child_group_id, root_trigger, root_ref, depth, outcome, reason, configured_by}`. `RunRequest.Chain` carries the row's own depth and path; `EnqueueRunWith` extends them (Task 3).

- [ ] **Step 1: Failing unit tests** (fakes): `TestStarterStarts` (request fields exact, row started, audit outcome `started`); `TestStarterTooDeep`; `TestStarterCycleOnPath`; `TestStarterDeletedTemplate`; `TestStarterWaitsForActiveRunWithoutCountingAttempts` (6 ticks active then idle → started, attempts 0); `TestStarterMaintenanceSkip`, `TestStarterMaintenanceExclude`, `TestStarterMaintenanceAllExcluded`, `TestStarterMaintenanceIgnoredForManualRoot`, `TestStarterDeletedScheduleIgnoresWindows`; `TestStarterNoHostsInScope`; `TestStarterRetriesThenGivesUp` (5 errors → skipped `could not start: …`, backoff instants exact).
- [ ] **Step 2: Run** `go test ./internal/chain -run Starter -timeout 900s` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Integration** `TestStarterTwoStartersStartOnce`: one due row, two starters tick concurrently through a barrier with a real `jobs.Service` → exactly one run group, row started once, one audit event.
- [ ] **Step 5: Run** `go test ./internal/chain -timeout 900s` — PASS.
- [ ] **Step 6: Commit** `feat(chain): start due follow-ups`

### Task 6: Notifications for chained runs

**Files:**
- Modify: `internal/notify/event.go` (`Event.FollowUpOf *NamedRef json:"follow_up_of,omitempty"`; outcomes), `internal/notify/outbox.go`, `internal/notify/render.go` (+ placeholders if bodies list fields), tests `outbox_test.go`, `render_test.go`
- Modify: `internal/db/queries/notifications.sql` if `NotifyScheduleContext` needs a variant that takes the follow-up template

**Interfaces:**
- `Outbox.RunGroupFinished` also handles `r.Trigger == jobs.TriggerChain`: looks up the row whose `child_group_id = r.GroupID` (`GetFollowUpByChild`); only when `root_trigger = 'schedule'` and the schedule exists: same event as a scheduled run, `Template` = the follow-up template, `FollowUpOf` = the parent run's template, `Schedule` = the root schedule, dedupe key `run:<group>`.
- `type FollowUpDecision struct { OrgID, ScheduleID, RowID, TemplateID, ParentTemplateID uuid.UUID; Category Category; Reason string; DecidedAt time.Time }` and `func (o *Outbox) FollowUpDecision(ctx context.Context, tx pgx.Tx, d FollowUpDecision) error` — event kind `EventScheduleSkipped` (category skipped) or `EventScheduleFailed` (category failure), `Decision{Outcome: "follow_up_skipped" | "follow_up_failed", Reason, DueAt: DecidedAt}`, dedupe key `chain:<row id>`.
- Starter mapping (Task 5 calls it when `root_trigger = schedule`): maintenance skips → skipped; deleted template, cycle, depth, given up → failure; everything else → no call.
- Text renderers add one line `Follow-up of <parent template>` when `FollowUpOf` is set.

- [ ] **Step 1: Failing tests:** `TestOutboxNotifiesChainedRunUnderRootSchedule`; `TestOutboxIgnoresChainedRunOfManualRoot`; `TestOutboxFollowUpDecisionCategories`; `TestRenderFollowUpOfLine`.
- [ ] **Step 2: Run** `go test ./internal/notify -timeout 900s` — FAIL.
- [ ] **Step 3: Implement; wire `Starter.SetNotifier` call sites in Task 7.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** `feat(notify): notify follow-ups of scheduled runs`

### Task 7: API and wiring

**Files:**
- Modify: `internal/api/template_handlers.go` (`follow_ups` in create/update input and `templateDTO`; 409 mapping), `internal/api/job_handlers.go` (run body `follow_ups`; `trigger` enum gains `chain`; `jobDetailDTO.Chain`; dismiss route), `internal/api/api.go` (`Deps.Chain *chain.SQLStore` or a small `ChainReader` interface declared in api), `internal/app/app.go` (wire planner, sinks, starter loop, notifier), `internal/app/chain_ports.go` (`ScheduleGuards`, `TemplateNames` adapters), `internal/api/apitest/apitest.go` (same wiring as production; starter not started — tests call `Tick`), `internal/app/options.go` (`WithChainTick` like `WithScheduleClock` if the e2e needs a short tick)
- Test: `internal/api/template_follow_ups_test.go`, `internal/api/job_chain_test.go`

**Interfaces (wire):**
- Template create/update body: `follow_ups?: [{template_id: uuid}]` (omitted on update = keep). Read: `follow_ups: [{template_id, template_name}]` (always present, `[]` when none).
- `POST …/job-templates/{id}/run` body gains `follow_ups?: [uuid]` (`nil` = template's; `[]` = none). Audit summary appends ` with follow-ups <names>` when overridden.
- `jobDetailDTO.Chain`: `{ parent: {group_id, first_job_id, template_id, template_name} | null, follow_ups: [{id, template_id, template_name, state, reason, child_first_job_id?, hosts}] }` — `follow_ups` ordered by position, always present.
- `POST /api/v1/orgs/{org}/jobs/{id}/follow-ups/dismiss` (`jobs.run`, `withAudit("dismissFollowUps", "job")`) → `{dismissed: int}`; 409 when the run group is already terminal.
- Template delete: `*templates.InUseError` → 409 with the constraint's text.
- Wiring in `app.go`: `chainStore := chain.NewSQLStore(appDB)`; `jobsSvc.SetFollowUpPlanner(chainStore)`; `jobsQueue.SetRunGroupSink(jobs.RunGroupSinks{outbox, chain.NewSink(chainStore, time.Now, slog.Default())})`; starter via `startLoop(starter.Run)` next to the schedule ticker, stopped on shutdown the same way.

- [ ] **Step 1: Failing apitest tests:** `TestTemplateFollowUpsRoundTrip` (create with [b], GET shows name; PUT without field keeps; PUT `[]` clears; revision bumps); `TestTemplateFollowUpCycle422` (detail message exact); `TestTemplateDeleteReferenced409`; `TestRunWithFollowUpOverride` (rows in DB match override; `[]` writes none); `TestRunFollowUpOverrideRejects` (self → 422); `TestJobDetailChainBlock` (parent run: follow_ups pending; after finishing it succeeded via the test queue and `Starter.Tick`, child detail has `parent`, trigger `chain`); `TestDismissFollowUps` (operator ok, viewer 403, terminal 409); `TestFollowUpsNeedJobsRun` (a token lowered below `jobs.run` but holding `templates.write` cannot set follow-ups — skip with a comment if no role allows that split today).
- [ ] **Step 2: Run** `go test ./internal/api -run 'FollowUp|Chain|Dismiss' -timeout 1800s` — FAIL.
- [ ] **Step 3: Implement; regenerate the SPA schema** `cd web && npm run api:gen`.
- [ ] **Step 4: Run** the same — PASS; `go test ./internal/app -timeout 900s` — PASS.
- [ ] **Step 5: Commit** `feat(api): follow-ups on templates, runs and the run detail`

### Task 8: End-to-end chain in `internal/e2e`

**Files:**
- Create: `internal/e2e/chain_test.go`
- Modify: `internal/e2e/templates_steps_test.go` (`createTemplateScoped(name, pbID, hostIDs []string, followUps []string) string`, `updateFollowUps(id string, ids []string) (status int, body string)`), `internal/e2e/steps_test.go` if a second host step is missing (`createManagedHost(name) string`, no SSH needed: it is never contacted)

**Scenario** (`TestChainEndToEnd`, fake runner mode, `wireScenario` with a short chain tick):
1. Second managed host `h2` in the scenario zone.
2. Template B scoped to {scenario host, h2}; template A scoped to {scenario host} with follow-up B.
3. Run A, `awaitJob` succeeded; await a job with `trigger == "chain"` (poll `GET /jobs?trigger=chain` until one exists, 60 s), await it terminal.
4. Assert: child `trigger_ref == A's run_group_id`; child `hosts == [scenario host]` (h2 left out); A's detail `chain.follow_ups[0].state == "started"` and `child_first_job_id` = the child; child detail `chain.parent.template_name == "A"`.
5. Failure case: flip A's var so the fake fails (the retry path's var flip), run A → failed; A's detail shows B `skipped` with reason `the run failed`; no new chain job within two ticks.
6. Cycle case: `updateFollowUps(B, [A])` → 422 with `follow-ups would form a cycle: B → A → B`.

- [ ] **Step 1: Write the test.**
- [ ] **Step 2: Run** `go test ./internal/e2e -run TestChainEndToEnd -timeout 900s` — PASS (the feature exists; if it fails, fix the cause in the owning task's code, not in the test).
- [ ] **Step 3: Commit** `test(e2e): job chaining end to end`

### Task 9: SPA — template editor and run dialogs

**Files:**
- Modify: `web/src/features/templates/TemplateDialog.tsx` (field), `web/src/features/templates/rebase.ts` (follow_ups in the three-way merge), `web/src/features/templates/TemplatesPage.tsx` (Run opens `RunWithFollowUpsDialog` when the template has follow-ups, runs directly as today when it has none), `web/src/features/runs/RunTemplateDialog.tsx` (follow-ups section once a template is chosen), `web/src/api/hooks/useTemplates.ts` (`useRunTemplateById` takes `followUps?: string[]`), `web/src/features/runs/trigger.ts` (`chain`), `web/src/i18n/en/*`, `web/src/i18n/de/*`
- Create: `web/src/features/templates/FollowUpsField.tsx` (MultiSelect of the org's templates minus `excludeId`, props `{ id, value, onChange, excludeId?, invalid?, describedBy? }`), `web/src/features/templates/RunWithFollowUpsDialog.tsx`
- Test: `FollowUpsField.test.tsx`, `TemplateDialog.test.tsx` (extend), `RunWithFollowUpsDialog.test.tsx`, `RunTemplateDialog.test.tsx` (extend)

Rules: the field's help text en "Started on the same hosts when a run of this template succeeds." / de "Startet auf denselben Hosts, wenn ein Lauf dieses Templates erfolgreich war."; label en "Follow-ups on success" / de "Folge-Läufe bei Erfolg"; trigger label en "Follow-up" / de "Folge-Lauf". The dialog sends `follow_ups` only when the selection differs from the template's (order-sensitive). A 422 whose detail starts with `follow-ups would form a cycle` shows inline under the field.

- [ ] **Step 1: Failing Vitest tests:** field excludes the template itself and keeps order; editor sends `follow_ups` and shows the cycle 422 inline; Templates page Run on a template with follow-ups opens the dialog, without follow-ups calls the API directly; dialog with unchanged selection posts no `follow_ups`, with one deselected posts `[]`, with one added posts `[b, c]`; host dialog shows the chosen template's follow-ups.
- [ ] **Step 2: Run** `cd web && npm run test -- features/templates features/runs` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same plus `npm run lint && npm run typecheck` (or the repo's equivalent script) — PASS.
- [ ] **Step 5: Commit** `feat(web): follow-ups in the template editor and run dialogs`

### Task 10: SPA — run detail

**Files:**
- Create: `web/src/features/runs/RunChainSection.tsx` (follow-ups list), `web/src/features/runs/RunChainSection.test.tsx`
- Modify: `web/src/features/runs/RunDetailPage.tsx` (section + nav entry when `chain.follow_ups.length > 0`), `web/src/features/runs/RunSidebar.tsx` (parent line "Follow-up of <template>" linking `/o/$org/runs/$first_job_id`; action "Do not start follow-ups" when some are pending, the run is active and `canRun`), `web/src/api/hooks/useJobs.ts` (`useDismissFollowUps(org, id)` invalidating the job query), i18n en/de
- Test: `RunSidebar.test.tsx` (extend), `RunDetailPage.test.tsx` (extend, via `runPageTestKit.tsx`)

Rules: state badges en/de — pending "Waiting"/"Wartet", due "Due"/"Fällig" (shows the reason, e.g. waiting for an active run), started "Started"/"Gestartet" with link to the child run, skipped "Skipped"/"Übersprungen" + reason, dismissed "Dismissed"/"Verworfen" + reason. Badge colours use the neutral/action palette, never status colours (brand rule: status colours are for states of hosts and checks). Section title en "Follow-ups" / de "Folge-Läufe". The section polls with the page while the run or any follow-up is not final.

- [ ] **Step 1: Failing tests:** section lists rows in order with state labels and reasons; started row links to the child; sidebar shows the parent link for `trigger: chain`; dismiss button only when pending + active + canRun, posts and refetches.
- [ ] **Step 2: Run** `cd web && npm run test -- features/runs` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS; `npm run lint`.
- [ ] **Step 5: Commit** `feat(web): show a run's chain on the run detail`

### Task 11: Playwright journey

**Files:**
- Create: `web/e2e/chain.spec.ts` (depends on the existing setup chain like the other journeys)

Journey: create template B, create template A with follow-up B in the editor, run A from the templates page through the dialog, open the run, wait for the "Follow-ups" section to show B as Started, follow the link, see "Follow-up of A" in the sidebar. Use `e2e/pickOption.ts` for the picker.

- [ ] **Step 1: Write the spec.**
- [ ] **Step 2: Run** `cd web && npm run e2e -- chain.spec.ts` — PASS.
- [ ] **Step 3: Commit** `test(web): job chaining journey`

### Task 12: End-user docs (noix-docs)

**Files** (worktree `~/_dev/.worktrees/noix-docs-job-chaining`, branch `docs/pulsix-job-chaining` cut from `pulsix`):
- Modify: `src/content/docs/pulsix/user/playbooks-and-jobs.md` (section "Follow-up runs": what it does; the hosts rule — the follow-up's own scope, only the hosts the first run went through, nothing passed on; whole run must succeed; waiting for an active run; cycles and the delete refusal; the run dialog's override; retry starts follow-ups on the retried hosts; the run detail lines and "Do not start follow-ups"), `user/schedules.md`, `user/maintenance-windows.md`, `user/notifications.md` (one short paragraph each, per the spec).
- No tech stack named (no product or library names, no "Ansible" beyond what the page already says — check the page's existing wording and stay within it).

- [ ] **Step 1: Write the pages; build the docs site locally if the repo has a check script (`npm run build` or `npm run check`).**
- [ ] **Step 2: Commit** in noix-docs `docs(pulsix): follow-up runs`

### Task 13: Close-out

**Files:** `docs/BACKLOG.md` (row 2.121 → `done` with the date, branch and docs commit; bump "Last updated"). `CHANGELOG.md` is generated by the release workflow from the commits; do not edit it.

- [ ] **Step 1: Run** `make test` (all groups) and `golangci-lint run` and `cd web && npm run lint && npm run test && npm run build` — all PASS.
- [ ] **Step 2: Run** `go test ./internal/e2e -timeout 900s` on its own — PASS.
- [ ] **Step 3: Update the backlog row and commit** `docs: job chaining done (backlog 2.116)`.
