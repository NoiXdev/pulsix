# Playbook dry run and outline — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** a playbook's page can dry-run it (`--check`, `--diff`, tags, chosen hosts, optionally the unsaved draft) through a worker and show what would change, and shows a server-side outline of editor playbooks.

**Architecture:** a dry run is an ordinary template run (`kind=run`, `trigger=manual`) with `jobs.check_mode=true`; flags, tags and the draft travel in the sealed run payload (`RunOverrides`), the claim hands them to the worker only if it announces support, the worker adds the ansible options. The outline is a pure Go function over yaml.v3 nodes behind one POST route. The SPA adds a Dry run tab, an Outline panel and dry-run marks on run lists and the run detail.

**Spec:** `docs/superpowers/specs/2026-11-22-playbook-dry-run-design.md` (binding, owner approved 2026-11-22). Mockup https://claude.ai/artifact/T9Mr66pttSSspSkqgyF2Bv.

## Global Constraints
- Go: layers per `CLAUDE.md`; sqlc via `PATH=$PATH:~/go/bin make sqlc`; protobuf via the repo's `make proto` (or the target the Makefile names); migrations additive only (`docs/upgrades.md`, version-skew rules); targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated (`cd web && npm run api:gen`). A new table column needs the backup round-trip fixture checked (`internal/backup` tests). Timestamps compared at µs precision.
- SPA: `cd web && npx vitest run && npx tsc -b && npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du", "Probelauf"); `src/ui` presentational; files under ~400 lines.
- Validation values (verbatim): tags at most 32, each `^[A-Za-z0-9_.:-]{1,64}$`; outline cap 2 000 entries (`truncated: true`); block depth 64; draft size `playbooks.MaxInlineBytes`; diff default true.
- TDD. No Playwright except Task 6; no Docker; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Backlog 3.126 marked done in Task 6.

## Review Focus
1. An old worker (claim without `supports_check_mode`) must never receive a `check_mode` job — test in Task 3.
2. A `Playbook` (draft) override without `Check` is refused by the service, whatever the caller — test in Task 2.
3. `host_ids` outside the template's resolved targets cannot widen the run: none left → 422 — test in Task 2.
4. A secret inside a `--diff` payload is redacted in the stored event — test in Task 3.
5. Outline on YAML that is valid but odd (unnamed tasks, `block` inside `rescue`, a play with only `roles`, tags as a string vs list, `import_playbook` at top level) yields sensible entries, never a 500 — table rows in Task 1.

---

## Task 1: Server — outline (spec §3)
**Files:** create `internal/playbooks/outline.go`, `outline_test.go`; API route in the playbook handlers file (`internal/api/playbook_handlers*.go`), its test; OpenAPI/schema regen.
**Produces:** `playbooks.Outline(content []byte) (Outline, error)`; `Outline{Plays []OutlinePlay; Tags []string; Counts OutlineCounts; Truncated bool}`, `OutlinePlay{Name, Hosts string; Become bool; Line int; Tags []string; Entries []OutlineEntry}`, `OutlineEntry{Section string /* pre_tasks|roles|tasks|post_tasks|handlers */; Kind string /* task|block|include|import|role */; Name string; Line, Depth int; Tags []string; RunsInCheckMode bool}`, `OutlineCounts{Plays, Tasks, Handlers int}`. Route `POST /api/v1/orgs/{org}/playbooks/outline` body `{content}` → the outline (JSON snake_case: `runs_in_check_mode`, …), permission `playbooks.read`, 422 with the `ValidateYAML` message on invalid input, body over `MaxInlineBytes` 422.
- [ ] Table tests first: named/unnamed task (module key as name), tags string vs list, inherited tags from play and block, `check_mode: false` on task, block and play (`RunsInCheckMode` false for all descendants; true otherwise), nested block in `rescue`/`always` with depth, `include_tasks`/`import_tasks`/`roles` entries not followed, handlers counted separately, top-level `import_playbook` as a play-level entry, cap → `Truncated`, sorted unique tag union, lines 1-based.
- [ ] API tests: 200 shape, 422 invalid, permission (reader ok, no-membership 403/404 as the other playbook routes).
- [ ] Implement, regen, lint, commit `feat(playbooks): server-side playbook outline`.

## Task 2: Server — start a dry run, the job's check_mode (spec §1)
**Files:** migration `internal/db/migrations/NNNNN_jobs_check_mode.sql` (`ALTER TABLE jobs ADD COLUMN check_mode boolean NOT NULL DEFAULT false`); `internal/db/queries/jobs.sql` (insert + list filter + DTO select); `internal/jobs/run_request.go` (`RunOverrides` gains `Check bool`, `Diff bool`, `Tags []string`, `Playbook *string`, all `json:",omitempty"`); `internal/jobs` service validation; `internal/api` new handler file `playbook_dry_run_handlers.go`; runs list `check_mode` query param; job DTO `check_mode`; retry-failed refusal; audit; backup fixture.
**Consumes:** nothing from Task 1. **Produces:** `jobs.RunRequest.Overrides.Check` etc. read by Task 3's claim; job DTO field `check_mode bool`; list param `check_mode=true|false`; `POST /api/v1/orgs/{org}/playbooks/{id}/dry-run` body `{template_id, host_ids?, tags?, diff?, draft?}` → 202 `jobStartOutput`.
- [ ] Service tests: `Playbook` without `Check` → error (sentinel, 422 at the API); tags validated (33 tags, a tag with a comma or space, 65 chars → 422); `host_ids` narrowing via `OnlyHosts` and none left → 422; the job row has `check_mode` true; no follow-ups/chain planned for a check run; the sealed payload round-trips the new fields.
- [ ] API tests: template whose playbook is not `{id}` → 422; repo playbook + `draft` → 422; invalid draft YAML → 422; oversize draft → 422; `jobs.run` without `playbooks.write`: dry run ok, with `draft` 403; viewer 403; audit row with `check_mode`, tags, host count and `draft_sha256` (never the text); `GET /runs?check_mode=true` returns only dry runs, `false` only real ones, omitted both; retry-failed of a dry run → 409 with a reason; job detail carries `check_mode`.
- [ ] Find run-outcome hooks (notifications on run failure, chain steps, follow-ups) and make dry runs skip them, each with a test; list what was found in the report.
- [ ] Regen sqlc/OpenAPI/schema, backup fixture, lint, commit `feat(jobs,api): start a playbook dry run`.

## Task 3: Claim and worker — check, diff, tags, draft, recap counts (spec §1 claim/version skew, §2)
**Files:** `proto/pulsix/worker/v1/worker.proto` (`ClaimJobRequest.supports_check_mode bool`; `Job.check bool`, `Job.diff bool`, `repeated string tags`), regenerated `internal/pb`; `internal/jobs/queue_sql.go` + `jobs.sql` claim query (`AND (NOT j.check_mode OR @supports_check_mode)`), claim passes draft instead of `playbookRef(tmpl)` when the override has `Playbook` and `Check`; `internal/grpcsrv/claim.go` + ports/fake; `internal/worker/loop.go` (announce support); `internal/worker/runner/runner.go` (args), `stats.go` (`host_counts`).
**Consumes:** Task 2's `RunOverrides` fields and `jobs.check_mode`. **Produces:** `jobs.stats.host_counts: {<host-id>: {ok, changed, skipped, failed, unreachable}}` for every run (read by Tasks 4–5).
- [ ] Tests: a claim with `supports_check_mode=false` never gets a check job (it stays queued, a real job behind it is still claimed); with true it gets it with `check`, `diff`, `tags` and the draft as playbook; a non-check job never carries a draft even if a payload were forged (claim ignores `Playbook` without `Check`); runner builds argv containing `--check`, `--diff`, `--tags a,b` through ansible-runner 2.4.3's supported mechanism (verify `--cmdline` vs `env/cmdline` in that version's source/docs and say which in the report) and none of them for a normal job; `buildStats` from a `playbook_on_stats` fixture yields `host_counts` with the five counts and the existing lists unchanged; a credential secret inside an event's `res.diff` is redacted in what the worker sends.
- [ ] If `internal/e2e` has a real-ansible template run test, add a check-mode sibling: a task that would create a file → after the dry run the file does not exist and the host's `changed` count is 1. Run only `internal/e2e` on its own invocation per `scripts/test-groups.sh`.
- [ ] Regen, lint, commit `feat(worker,jobs): run dry runs in check mode on workers that support it`.

## Task 4: SPA — outline, header, Dry run tab (spec §4 first three bullets)
**Files:** `web/src/features/playbooks/` — `usePlaybookOutline.ts` (debounced ~400 ms POST, keeps the last good outline), `PlaybookOutlinePanel.tsx` (in `PlaybookSidePanels`), header chips/summary in `PlaybookHeader.tsx` + **Dry run…** button, `playbookTabs.ts` gains `dry-run` between `editor` and `runs`, `PlaybookDryRunTab.tsx` (form), `DryRunResult.tsx` (latest dry run), `web/src/ui/diff/lineDiff.ts` (pure line diff → unified hunks) + tests; `CodeEditor` gains a way to move the cursor to a line and focus (`revealLine` via ref or prop) if it lacks one.
**Consumes:** Task 1 route, Task 2 route and `check_mode`/list filter, Task 3 `host_counts`, existing run events route/SSE.
- [ ] Tests: outline panel lists entries, click calls reveal on that line, warning marker on `runs_in_check_mode=false`, repo playbook shows the note; header chips and counts; tab routing `?tab=dry-run`; form: template select limited to templates using the playbook (none → create link), host picker optional, tag toggles from the outline, diff on by default, draft checkbox only for an editor playbook while dirty and with `playbooks.write`, warning lists the real-running tasks, submit posts the exact body and shows the started run; without `jobs.run` no button/tab form (notice); latest dry run: picks the newest `check_mode` run of this playbook, sums counts over hosts, lists changed tasks per host with diffs (`before`/`after` via `lineDiff.ts`, `prepared` as is, large collapsed), live while running; `lineDiff.ts` table tests (equal, insert, delete, replace, trailing newline, size cap).
- [ ] Commit `feat(web): playbook dry run tab and outline`.

## Task 5: SPA — runs lists and run detail (spec §4 Runs bullet)
**Files:** `web/src/features/runs/` (runs list filter + badge, `runsSearch.ts`), the playbook Runs tab, `RunDetailPage.tsx`/`RunResultSection.tsx`/`status.ts` (banner, counts from `host_counts` with fallback to the booleans for older runs), `EventLog.tsx` (diffs via Task 4's `ui/diff/lineDiff.ts`).
- [ ] Tests: **Dry runs** filter maps to `check_mode=true` in the URL/search and request; badge on dry-run rows; run detail banner with tags and "draft"; counts shown when `host_counts` present, today's booleans when absent; an event with `res.diff` renders the diff.
- [ ] Commit `feat(web): mark dry runs in run lists and the run detail`.

## Task 6: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, full Playwright e2e (add one spec: start a dry run from the playbook page and see the result section, if the e2e stack has a worker; else note why not), docs on the docs scratch branch (playbooks page: Dry run tab, Outline; runs: dry-run filter), backlog 3.126 done, merge to local main and redeploy the local stacks.
