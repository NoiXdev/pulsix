# Follow-ups of approved run requests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An organization setting (default off) decides whether an approved run request starts the template's follow-ups; when on, the approval page shows them, the fingerprint covers them and the approver may deselect.

**Architecture:** A boolean on `org_assistant_settings`; the run picture gains an optional `follow_ups` block built by a pure tree walk in `internal/runrequests`; `Approve` turns the picture plus the approver's selection into an explicit follow-up list that the enqueue paths take verbatim (`EnqueueRunWith` override, filtered `CopyFollowUps`), after clearing unstarted rows of a failed earlier attempt. The SPA renders the block and the new switch.

**Tech Stack:** Go (huma, sqlc, goose, Postgres testdb), React + TanStack Query + Vitest.

**Spec:** `docs/superpowers/specs/2026-11-16-approved-run-follow-ups-design.md` (approved 2026-11-16). Read it before any task.

## Global Constraints

- Everything in the repo is English; GUI strings in en and de (`web/src/i18n/{en,de}/*.ts`).
- Layers per `CLAUDE.md`: handlers decode/call/encode only; services depend on interfaces next to them; stores are sqlc behind those interfaces.
- Migrations additive only; the new one is `internal/db/migrations/00106_approved_run_follow_ups.sql`.
- TDD: failing test first. Run Go tests with `make test` (or `go test -race -timeout 900s ./internal/<pkg>/...`, `internal/api` with `-timeout 1800s`), never a bare whole-module `go test ./...`.
- `golangci-lint run` and `gofmt -s -l .` clean before every commit; `cd web && npm run lint && npm test` clean for SPA commits.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.
- Public texts (noix-docs) name no tech stack.
- Default mode is "start none" (`false`) for every organization, absent row included.
- A picture without follow-ups must hash exactly as before: golden values (request id `01930000-0000-7000-8000-000000000001`, picture in Task 3) are `343fc0714d93b72e718bad778a2a2b696344b63db23f729f61cef298d2df496b` (run) and `308c74fdd6b900669e61fb8e20b834f34ed3b6fd47813a3110d72105497e25e9` (with the retry block).
- `MaxPictureFollowUps = 100`; chain depth rule mirrors `internal/chain/starter_decide.go` (`maxDepth = 10`, skip when the row's depth ≥ 10 or its template is on its path).

## Review Focus

- Re-approval after a failed enqueue in the other mode or with a different selection: the second attempt must start exactly its own selection (Task 4 test `TestRunClearsUnstartedFollowUpsFirst`, Task 5 API test).
- An approve body without `follow_ups` from an old client when the picture is truncated: must start none (Task 5 `TestApproveSelection` case "truncated, omitted").
- A follow-up template deleted between the picture and the enqueue: the enqueue's override check refuses (422 path through `mapJobsErr`), request reverts to pending (Task 5 API test asserts status pending afterwards).
- A diamond (X→Y, X→Z, Y→W, Z→W): W appears under both, counted twice toward the cap, never marked `in_chain` (Task 3 table case "diamond").
- Settings PUT from an older SPA sending only `assistant_enabled`: the follow-ups value stays (Task 1 API test).

---

### Task 1: The setting (migration, store, service, route)

**Files:**
- Create: `internal/db/migrations/00106_approved_run_follow_ups.sql`
- Modify: `internal/db/queries/assistant.sql`, regenerate `internal/db/gen` (`make sqlc`)
- Modify: `internal/assistant/service.go`, `internal/assistant/store_sql.go`
- Modify: `internal/api/assistant_settings_handlers.go`
- Test: `internal/assistant/store_sql_test.go`, `internal/api/assistant_settings_handlers_test.go`

**Interfaces:**
- Produces: `assistant.Store` / `*assistant.Service` gain `StartsFollowUps(ctx, orgID uuid.UUID) (bool, error)` and `SetStartsFollowUps(ctx, orgID uuid.UUID, on bool) error`; `api.AssistantSettings` gains the same two methods. DTO field `ApprovedRunsStartFollowUps` JSON `approved_runs_start_follow_ups`.

- [ ] **Step 1: Migration.** `ALTER TABLE org_assistant_settings ADD COLUMN approved_runs_start_follow_ups boolean NOT NULL DEFAULT false;` with a header comment naming the spec; Down drops the column.
- [ ] **Step 2: Queries.** `GetOrgAssistantStartsFollowUps :one` (select the column), `UpsertOrgAssistantStartsFollowUps :exec` (`INSERT … (org_id, approved_runs_start_follow_ups) VALUES … ON CONFLICT (org_id) DO UPDATE SET approved_runs_start_follow_ups = EXCLUDED…, updated_at = now()`). The existing `UpsertOrgAssistantEnabled` already leaves other columns alone; check it does. `make sqlc`.
- [ ] **Step 3: Failing store test** `TestStartsFollowUps` in `internal/assistant/store_sql_test.go`: absent row → false; `SetStartsFollowUps(true)` → true and `Enabled` still false; `SetEnabled(true)` afterwards keeps `StartsFollowUps` true; RLS: another org reads false.
- [ ] **Step 4: Implement** the store methods (no row → false, as `Enabled`) and the service pass-throughs. Run the test: PASS.
- [ ] **Step 5: Failing API test** `TestAssistantSettingsFollowUps` in `internal/api/assistant_settings_handlers_test.go`: GET returns `approved_runs_start_follow_ups: false`; admin PUT `{assistant_enabled: false, approved_runs_start_follow_ups: true}` → 200 and true; PUT `{assistant_enabled: true}` (field omitted) → still true; operator PUT → 403; token PUT → refused as for the switch; audit summary of the second PUT names the follow-up change ("approved run requests start follow-ups").
- [ ] **Step 6: Implement.** Split the DTO: read form has both fields as `bool`; PUT body has `AssistantEnabled bool` (required, as now) and `ApprovedRunsStartFollowUps *bool` (`json:",omitempty"`, doc "Whether an approved run request also starts the template's follow-ups; omitted keeps it"). Call `SetEnabled` as today, and `SetStartsFollowUps` only when the pointer is non-nil. Summary: join the parts that changed against the value read before the write with "; " ("turned assistant access on", "approved run requests start follow-ups" / "approved run requests start no follow-ups"); unchanged PUT keeps today's switch summary. Run: PASS.
- [ ] **Step 7: Commit** `feat(assistant): setting for follow-ups of approved run requests`.

### Task 2: Filtered copy, clearing unstarted rows, retry group (chain + jobs)

**Files:**
- Modify: `internal/db/queries/chain.sql` (+ `make sqlc`), `internal/chain/store_sql.go`
- Modify: `internal/jobs/follow_ups.go`, `internal/jobs/service.go` (`RetryFailed`, `RetryFailedInto`, `RetryTargets`)
- Test: `internal/chain/store_sql_test.go`, `internal/jobs/follow_ups_test.go`, `internal/jobs/retry_targets_test.go`

**Interfaces:**
- Produces:
  - `jobs.FollowUpPlanner.CopyFollowUps(ctx, orgID, fromGroupID, toGroupID uuid.UUID, configuredBy *uuid.UUID, only *[]uuid.UUID) error` — nil copies every row, non-nil only rows whose template is listed, empty none.
  - `(*chain.SQLStore).ClearUnstarted(ctx, orgID, groupID uuid.UUID) error` — deletes the group's rows in state `pending` or `skipped`.
  - `(*jobs.Service).RetryFailedInto(ctx, orgID, id uuid.UUID, actor *uuid.UUID, group uuid.UUID, followUps *[]uuid.UUID) ([]Job, error)`; `RetryFailed` passes nil.
  - `(*jobs.Service).RetryTargets(ctx, orgID, id uuid.UUID) (templateID, runGroupID uuid.UUID, hosts []uuid.UUID, err error)` — the finished job's own run group.

- [ ] **Step 1: Queries.** `CopyRunFollowUps` gains `AND (sqlc.narg(template_ids)::uuid[] IS NULL OR r.template_id = ANY(sqlc.narg(template_ids)::uuid[]))`. New `ClearUnstartedRunFollowUps :execrows`: `DELETE FROM run_follow_ups WHERE org_id = @org_id AND parent_group_id = @parent_group_id AND state IN ('pending', 'skipped')`. `make sqlc`.
- [ ] **Step 2: Failing tests.** Extend `TestCopyFollowUps` with sub-tests "only listed" (two source rows, `only = [one]` → one row copied) and "empty list copies none". New `TestClearUnstarted`: rows in each of the five states in one group plus a pending row in another group; after `ClearUnstarted` only `due`, `started`, `dismissed` remain in the group and the other group is untouched. `TestRetryTargets` asserts the returned group equals the job's `RunGroupID`. In `follow_ups_test.go`, `TestApprovedRunRequestPlansFollowUpsInItsGroup/retry` passes `nil` and expects the fake planner to see `only == nil`; new `TestRetryFailedIntoFiltersFollowUps` passes `&[]uuid.UUID{x}` and asserts the fake received exactly that.
- [ ] **Step 3: Run** `go test -race -timeout 900s ./internal/chain/... ./internal/jobs/...`: compile failure / FAIL.
- [ ] **Step 4: Implement**: store methods (`db.NullUUIDs`-style nil for the array when `only == nil`; use whatever helper the package already has for nullable arrays, else pass `nil` vs `[]uuid.UUID{}` directly), interface and fake planner signature updates, `RetryFailedInto`/`RetryFailed`, `RetryTargets` third value (update `internal/runrequests/resolve.go` callers to ignore it for now so the build stays green).
- [ ] **Step 5: Run** the same command: PASS. `golangci-lint run ./internal/chain/... ./internal/jobs/... ./internal/runrequests/...`.
- [ ] **Step 6: Commit** `feat(chain): copy only chosen follow-ups and clear unstarted ones`.

### Task 3: Follow-ups in the run picture (types, tree walk, fingerprint)

**Files:**
- Modify: `internal/runrequests/picture.go`
- Create: `internal/runrequests/follow_ups.go`
- Test: `internal/runrequests/follow_ups_test.go`, `internal/runrequests/service_test.go` (`TestFingerprintCoversEveryField`)

**Interfaces:**
- Produces:
  - `Picture.FollowUps *PictureFollowUps` (`json:"follow_ups,omitempty"`, **last** field), `PictureFollowUps{Start bool "start"; Templates []PictureFollowUp "templates"; Truncated bool "truncated,omitempty"}`, `PictureFollowUp{TemplateID "template_id"; TemplateName "template_name"; TemplateRevision int64 "template_revision,omitempty"; Stop string "stop,omitempty"; FollowUps []PictureFollowUp "follow_ups,omitempty"}` exactly as in the spec.
  - Stop values: `StopDeleted = "deleted"`, `StopInChain = "in_chain"`, `StopTooDeep = "too_deep"`; `MaxPictureFollowUps = 100`.
  - `type FollowUpTemplate struct{ Name string; Revision int64; FollowUps []uuid.UUID }`
  - `type FollowUpLookup func(ctx context.Context, id uuid.UUID) (t FollowUpTemplate, found bool, err error)`
  - `type FollowUpRoot struct{ TemplateID uuid.UUID; Name string; Depth int; Path []uuid.UUID }` — a direct follow-up, `Depth`/`Path` being its row's (the parent's) depth and path.
  - `func BuildFollowUps(ctx context.Context, start bool, roots []FollowUpRoot, lookup FollowUpLookup) (*PictureFollowUps, error)` — nil for no roots.
  - `func (f *PictureFollowUps) Startable() []uuid.UUID` — direct follow-ups without `Stop`, in order; empty when `!Start` or `Truncated`.

- [ ] **Step 1: Failing golden test** `TestFingerprintUnchangedWithoutFollowUps`: request id and picture below, both golden values from Global Constraints.

```go
p := Picture{
    TemplateID: "01930000-0000-7000-8000-000000000002", TemplateName: "cloudpanel", TemplateRevision: 3, Limit: "web-*",
    Playbook: PicturePlaybook{ID: "01930000-0000-7000-8000-000000000003", Source: "repo", Path: "cloudpanel.yml"},
    Targets: []PictureHost{{ID: "01930000-0000-7000-8000-000000000004", Name: "k10001-cp-01", Address: "10.0.0.1"}},
    VaultIdentities: []PictureVault{}, ExtraVars: map[string]any{"a": "b"}, VarsDigest: "d",
}
// then p.Retry = &PictureRetry{JobID: "01930000-0000-7000-8000-000000000005", FailedHosts: []string{"01930000-0000-7000-8000-000000000004"}}
```
  It passes today; it must keep passing after Step 4 (it guards the omitempty).
- [ ] **Step 2: Failing table test** `TestBuildFollowUps` with a map-backed lookup. Cases and expected trees:
  - no roots → nil;
  - start=false, roots [Y] → `{Start:false, Templates:[{Y, "listmonk"}]}`, no revision, no children, lookup never called;
  - start=true, X(depth1,path[X]) → Y(rev 4) → Z(rev 2): Y carries revision 4 and child Z with revision 2;
  - cycle X→Y→X: Y's child X is `{Stop:"in_chain"}` with no revision and no children;
  - root on its own path (retry rows): `Stop:"in_chain"`;
  - depth: a root whose row depth is 10 → `Stop:"too_deep"`; a chain from depth 1 of 11 distinct templates ends with the 10th node having one `too_deep` child (the 11th);
  - deleted: lookup returns found=false → `Stop:"deleted"`, name from the root;
  - diamond X→{Y,Z}, Y→W, Z→W: W under both, neither `in_chain`;
  - cap: 1 root with 10 children each with 10 children → exactly 100 nodes emitted, `Truncated: true`, `Startable()` empty;
  - lookup error → error returned.
  Plus `TestStartable`: stop-marked direct follow-ups excluded; `!Start` → empty.
- [ ] **Step 3: Extend** `TestFingerprintCoversEveryField` with mutations of `FollowUps`: flip `Start`, change a revision, swap two templates' order, add a child, set `Truncated`.
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/runrequests/...`: FAIL (undefined).
- [ ] **Step 5: Implement** the types and `BuildFollowUps` (depth-first in position order; a node counts toward the cap when emitted; the stop checks run before the lookup, in the order too_deep, in_chain, then lookup → deleted). `Fingerprint` needs no change.
- [ ] **Step 6: Run**: PASS, golden test included.
- [ ] **Step 7: Commit** `feat(runrequests): follow-ups in the run picture`.

### Task 4: Resolver: fill the picture, enqueue the chosen list

**Files:**
- Modify: `internal/runrequests/resolve.go` (split the follow-up part into `follow_ups.go` if `resolve.go` would pass ~400 lines), `internal/runrequests/service.go` (`Enqueuer`)
- Modify: `internal/app/app.go`, `internal/api/apitest/apitest.go` (`newRunRequests`)
- Test: `internal/runrequests/resolve_follow_ups_test.go` (new, fakes)

**Interfaces:**
- Consumes: Task 1 `StartsFollowUps`; Task 2 `ClearUnstarted`, `CopyFollowUps(only)`, `RetryFailedInto(…, followUps)`, `RetryTargets` group; Task 3 `BuildFollowUps`.
- Produces:
  - `Sources` gains `Settings FollowUpSettings` (`StartsFollowUps(ctx, orgID) (bool, error)`) and `FollowUpRuns FollowUpRunSource` (`ListForGroup(ctx, orgID, groupID) ([]chain.FollowUp, error)`; `ClearUnstarted(ctx, orgID, groupID) error`). `JobRunner` signatures follow Task 2.
  - `Enqueuer.Run(ctx, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, actor uuid.UUID, idempotencyKey string, group uuid.UUID, followUps []uuid.UUID) error`
  - `Enqueuer.RetryFailed(ctx, orgID, jobID, actor, group uuid.UUID, followUps []uuid.UUID) error`
  - Both resolver methods call `ClearUnstarted(group)` first, then enqueue with the list made non-nil (`EnqueueRunWith` gets `FollowUps: &list`; `RetryFailedInto` gets `&list`).

- [ ] **Step 1: Failing tests** in `resolve_follow_ups_test.go` with minimal fakes for every `Sources` member the paths touch:
  - `TestPictureFollowUpsOfARun`: template X with follow-ups [Y] (templates.FollowUp names), setting off → `{Start:false, Templates:[{Y, name}]}`; setting on → tree with Y's revision; template without follow-ups → `FollowUps == nil`.
  - `TestPictureFollowUpsOfARetry`: `RetryTargets` returns group G; `ListForGroup(G)` returns rows [Y depth 2 path [A,X]] → root uses depth 2 / path [A,X]; a row with empty `TemplateName` and a lookup miss → `Stop:"deleted"`; `RetryTargets` error → `FollowUps == nil`.
  - `TestRunClearsUnstartedFollowUpsFirst`: `Run(…, nil)` calls `ClearUnstarted(group)` before `EnqueueRunWith`, and `EnqueueRunWith` receives `FollowUps` non-nil and empty; `Run(…, [Y])` passes `&[Y]`. Same for `RetryFailed` → `RetryFailedInto` with `&[]` / `&[Y]`. A `ClearUnstarted` error aborts before the enqueue.
- [ ] **Step 2: Run**: FAIL.
- [ ] **Step 3: Implement.** In `Picture`: roots for `KindRun` from `tmpl.FollowUps` (depth 1, path `[tmpl.ID]`); for `KindRetryFailed` from `ListForGroup(group)` rows (their depth, path, `TemplateName`). Lookup via `Templates.Get` mapping `templates.ErrNotFound` to found=false and `t.FollowUps` to ids. `Start` from `Settings.StartsFollowUps`. Wire both new sources in `app.go` (`assistant` service, `chainStore`) and `apitest.newRunRequests` (`deps.Assistant`, `deps.Chain`; add `ClearUnstarted` to the `api.Deps.Chain` interface if `Deps.Chain` is an interface that lacks it, otherwise pass the store).
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/runrequests/... ./internal/app/...`: PASS (the service tests' `fakeEnqueuer` gets the new parameter in this task).
- [ ] **Step 5: Commit** `feat(runrequests): resolve and enqueue the follow-ups an approval chose`.

### Task 5: Approve with a selection, route, claim, MCP

**Files:**
- Modify: `internal/runrequests/service.go` (`Approve`), `internal/api/run_request_handlers.go`, `internal/mcp/tools_jobs.go`
- Test: `internal/runrequests/service_test.go`, `internal/api/run_request_handlers_test.go`, `internal/mcp/tools_jobs_test.go`
- Regenerate: OpenAPI + `web/src/api/schema.d.ts` (`cd web && npm run api:gen`)

**Interfaces:**
- Consumes: Task 3 `Startable()`, Task 4 `Enqueuer`.
- Produces:
  - `type Approved struct{ Request Request; FollowUps *PictureFollowUps; Started []PictureFollowUp }`
  - `func (s *Service) Approve(ctx context.Context, orgID, id, by uuid.UUID, fingerprint string, followUps *[]uuid.UUID) (Approved, error)`
  - Approve body: `Fingerprint string` (as now) and `FollowUps []string` `json:"follow_ups,omitempty"` with doc "Direct follow-ups to start, a subset of the picture's; omitted starts all of them when the organization allows follow-ups"; the handler maps nil slice → nil pointer, non-nil (even empty, use `*[]string` if huma cannot tell omitted from `[]`) → pointer.

- [ ] **Step 1: Failing service tests.**
  - `TestApproveSelection` (table; picture from `fakePictures` with a `FollowUps` block, fingerprint matching): mode B omitted → enqueuer list `[]`; mode B `[Y]` → `ErrInvalidInput`, request still pending, enqueuer not called; mode A omitted → all startable in order; mode A `[Z]` of `[Y, Z]` → `[Z]`; `[Z, Y]` → `[Y, Z]` (picture order); foreign id → `ErrInvalidInput`; stop-marked id → `ErrInvalidInput`; truncated, omitted → `[]`; truncated `[Y]` → `ErrInvalidInput`; no follow-ups block, omitted → `[]`; a changed fingerprint still wins with `ErrChanged` before any selection check. `Approved.Started` holds the started nodes.
  - `TestCheckRunClaim` gains: approved in mode A, then the picture's follow-up revision changes → `ErrChangedAfterApproval`; setting flipped → same.
- [ ] **Step 2: Run**: FAIL. **Step 3: Implement** `Approve` per the spec's "Approve" section; validation after the fingerprint check and before `Decide`. Run: PASS.
- [ ] **Step 4: Failing API tests** in `run_request_handlers_test.go` (use the existing run request fixture; give the template a follow-up template through the template update route as an operator; drive the chain with `s.ChainStarter` where needed):
  - `TestRunRequestFollowUpsOff`: detail has `picture.follow_ups.start == false` and the template listed; approve; the run's `run_follow_ups` list (`GET /jobs/{id}` `chain.follow_ups`) is empty.
  - `TestRunRequestFollowUpsOn`: admin turns the setting on; detail lists the tree with revisions; approve without `follow_ups` → chain block lists the follow-up `pending`; a second request approved with `follow_ups: []` → none; with a foreign id → 422 and the request still pending.
  - `TestRunRequestFollowUpSettingChangedRefuses`: load detail, flip the setting, approve with the old fingerprint → 409.
  - `TestRunRequestReapprovalUsesItsOwnSelection`: setting on, make the enqueue fail once (e.g. the only target host made unmanaged so the scope is empty), approve with default → error, request pending; restore the host, approve with `follow_ups: []` → the run's chain block lists no follow-up.
  - audit summary of an approval with follow-ups: "approved run request (template revision N; follow-ups: <name>)"; with all deselected: "…; no follow-ups".
- [ ] **Step 5: Implement** the route (body field, audit summary from `Approved`). Run `go test -race -timeout 1800s ./internal/api/...`: PASS.
- [ ] **Step 6: MCP.** Add to the `request_run` description: "Whether an approval also starts the template's follow-ups is the organization's setting; the request detail says." Test in `tools_jobs_test.go`: `get_run_request` result text contains `follow_ups` for a template with one. Run `go test -race -timeout 900s ./internal/mcp/...`: PASS.
- [ ] **Step 7: Regenerate** `cd web && npm run api:gen`; `git diff --stat web/src/api/schema.d.ts` shows `PictureFollowUps`, `PictureFollowUp`, `approved_runs_start_follow_ups`, approve `follow_ups`.
- [ ] **Step 8: Commit** `feat(api): approve run requests with a follow-up selection`.

### Task 6: SPA — approval page and settings switch

**Files:**
- Modify: `web/src/api/hooks/useRunRequests.ts`, `web/src/api/hooks/useAssistantSettings.ts`
- Create: `web/src/features/runs/RunRequestFollowUps.tsx`
- Modify: `web/src/features/runs/RunRequestDetailPage.tsx`, `web/src/features/settings/AssistantCard.tsx`
- Modify: `web/src/i18n/en/runs.ts`, `web/src/i18n/de/runs.ts`, `web/src/i18n/en/settings.ts`, `web/src/i18n/de/settings.ts`
- Test: `web/src/features/runs/RunRequestDetailPage.test.tsx`, `RunRequestDetailPage.de.test.tsx`, `web/src/features/runs/RunRequestFollowUps.test.tsx`, `web/src/features/settings/OrgGeneralPage.test.tsx` (or a new `AssistantCard.test.tsx`)

**Interfaces:**
- Consumes: Task 5 schema types `components['schemas']['PictureFollowUps']`.
- Produces:
  - `useApproveRunRequest(org, id).mutateAsync({ fingerprint: string; followUps?: string[] })` — `follow_ups` sent only when `followUps` is defined.
  - `useUpdateAssistantSettings(org).mutateAsync({ assistant_enabled: boolean; approved_runs_start_follow_ups?: boolean })` — callers updated.
  - `RunRequestFollowUps({ followUps, deselected, onToggle, disabled })` in `web/src/ui`-free feature code (no data fetching).

- [ ] **Step 1: Failing Vitest tests.**
  - `RunRequestFollowUps.test.tsx`: mode off renders the note (en: "Approving starts none of these follow-ups. Your organization has turned that off in Settings → General.") and names, no checkboxes; mode on renders one checked checkbox per startable direct follow-up with name and revision, children indented read-only with revisions; stop labels "already in this chain", "chain too long", "template deleted" without checkbox; truncated renders "More follow-ups than this page can show; approving starts none of them." and no enabled checkbox; the hint "Follow-ups start later, when this run succeeds, on the hosts it ran on. This page shows them by name and revision, not what they run." is shown when on.
  - `RunRequestDetailPage.test.tsx`: nav item "Follow-ups" only when `picture.follow_ups`; plain approve posts `{fingerprint}` without `follow_ups`; unchecking one posts `follow_ups` with the rest; after a "changed" 409 and refetch the selection resets to all checked.
  - de test: the German strings render.
  - Settings: second switch "Approved runs start follow-ups" with the spec's hint; toggling sends `{assistant_enabled: <current>, approved_runs_start_follow_ups: true}`; disabled for a role without `org.settings.write`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/runs src/features/settings`: FAIL.
- [ ] **Step 3: Implement** the hooks, component, page wiring (local `Set<string>` of deselected ids, reset on `fingerprint` change), card switch and catalogue strings (en as quoted in the spec and Step 1; de translated in the catalogue's existing tone).
- [ ] **Step 4: Run** `cd web && npm run lint && npm test`: PASS; coverage stays ≥ 70 %.
- [ ] **Step 5: Commit** `feat(web): follow-ups on the approval page and their setting`.

### Task 7: Docs (repo and noix-docs)

**Files:**
- Modify: `docs/upgrades.md` (a paragraph after the job chaining one), `docs/BACKLOG.md` (row 2.122, done, spec and plan paths, docs commits)
- noix-docs, worktree `~/_dev/.worktrees/noix-docs-approved-follow-ups` on new branch `docs/pulsix-approved-follow-ups` from `docs/pulsix-assistant-access`: `src/content/docs/pulsix/user/assistant-access.md`
- noix-docs, worktree `~/_dev/.worktrees/noix-docs-approved-follow-ups-note` on new branch `docs/pulsix-approved-follow-ups-note` from `pulsix`: `src/content/docs/pulsix/user/playbooks-and-jobs.md`

- [ ] **Step 1: upgrades.md** — the spec's "Upgrades" text, adapted to the file's style (migration 00106, default off, which fingerprints change, queued approved runs with follow-ups fail their claim, skew, the bold rule "Approve run requests with follow-ups only once every manager runs this version.", backup carries the column).
- [ ] **Step 2: BACKLOG.md** row 2.122 after 2.121.
- [ ] **Step 3: noix-docs.** Create both worktrees with `git -C ~/_dev/noix-docs worktree add -b <branch> <path> <base>` (never switch the main checkout, it is on another branch). `assistant-access.md`: a section on the setting (where, who may change it, default off), what the approval page shows in each mode, deselecting, that follow-ups start later on the hosts the run played on, and the limits (shown by name and revision only; a template an assistant allowed to edit templates changes after the approval runs as changed). `playbooks-and-jobs.md` follow-up section: one sentence that an approved assistant run request starts follow-ups only when the organization allows it, linking to the assistant page. No tech stack.
- [ ] **Step 4: Commit** in each repo: `docs: follow-ups of approved run requests` (pulsix), `docs(pulsix): follow-ups of approved run requests` and `docs(pulsix): note on approved run requests and follow-ups` (noix-docs). Put the noix-docs commit hashes into the BACKLOG row (amend-free: a second pulsix commit `docs: name the docs commits for 2.122`).

### Task 8: Whole-branch verification

- [ ] **Step 1:** `make test` (all groups) — PASS.
- [ ] **Step 2:** `golangci-lint run` and `gofmt -s -l .` — clean.
- [ ] **Step 3:** `cd web && npm run lint && npm test -- --coverage` — PASS, SPA ≥ 70 %.
- [ ] **Step 4:** Go coverage of `internal/runrequests` and `internal/assistant` ≥ 80 % (`go test -cover`).
- [ ] **Step 5:** Whole-branch review (requesting-code-review), fix findings, re-run Steps 1–3.
