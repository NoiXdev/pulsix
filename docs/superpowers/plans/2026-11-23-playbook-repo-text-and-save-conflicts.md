# Playbook repository text and save conflicts — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** a repository playbook's page shows its YAML as of the last sync (read-only, with Check, Variables, Outline), and saving an editor playbook that someone else saved meanwhile answers 412 with a notice instead of overwriting.

**Architecture:** (a) the claim of a sync job asks the worker for texts (`Job.want_playbook_texts`); the worker ships `{path: text}` under the result stats key `playbook_texts` (plus `playbook_texts_left_out`), the manager takes both keys out of the stats in the finish path that already takes `var_sources` out, and stores each text on its `repo_playbook_sources` row; a new read route serves it. (b) `playbooks.revision` with ETag / optional `If-Match` on the playbook GET/PUT, the template pattern; the SPA draft hook carries the base revision and the page shows a conflict notice.

**Tech Stack:** Go (huma, sqlc, goose, gRPC/protobuf), React + TypeScript (Vitest, Playwright).

**Spec:** `docs/superpowers/specs/2026-11-23-playbook-repo-text-and-save-conflicts-design.md` (binding, owner approved 2026-11-23).

## Global Constraints
- Go: layers per `CLAUDE.md`; sqlc via `PATH=$PATH:~/go/bin make sqlc`; protobuf via the Makefile's proto target; migrations additive only (`docs/upgrades.md`, version-skew rules), next numbers 00117+; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated (`cd web && npm run api:gen`). New columns: backup round-trip fixture checked (`internal/backup` tests). Timestamps compared at µs precision.
- SPA: `cd web && npx vitest run && npx tsc -b && npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); `src/ui` presentational; files under ~400 lines.
- Values (verbatim): per text at most `varsource.MaxFileBytes` (512 KiB); all texts of one sync at most **4 MiB** (`MaxPlaybookTextsBytes = 4 << 20`, in `internal/varsource`); left-out reasons `too_large`, `budget`, `not_utf8`, `unreadable`; API-only reason `not_sent`; stats keys `playbook_texts`, `playbook_texts_left_out`; revision column `playbooks.revision bigint NOT NULL DEFAULT 1`.
- TDD. No Playwright except Task 5; no Docker; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Backlog 3.127 marked done in Task 5.

## Review Focus
1. An older manager must never receive texts (a claim without `want_playbook_texts` → the result carries neither key) — test in Task 1.
2. The texts never end up in `jobs.stats` on the new manager, even for a failed or cancelled sync — test in Task 2.
3. A file reached through a symlink leaving the subpath, or under `.git`, is `unreadable`, never read — test in Task 1.
4. A sync whose report is Failed keeps the previous rows' texts; a successful sync from an older worker replaces them with NULL (no stale text shown as current) — test in Task 2.
5. A PUT without `If-Match` still saves (API clients), a stale one leaves the text and revision unchanged — test in Task 3.

---

## Task 1: Worker — read and ship the playbook texts (spec (a) Worker → manager)
**Files:** `proto/pulsix/worker/v1/worker.proto` (`Job.want_playbook_texts bool = 25`), regenerated `internal/pb`; `internal/varsource` (constant `MaxPlaybookTextsBytes`, left-out reason constants); `internal/worker/sync_texts.go` (new) + test; `internal/worker/sync.go` (call it after `readVarSources`, only with the flag); claim side: where the claim builds a `KindSyncRepo` job (`internal/jobs/queue_sql.go` near line 503 / `internal/grpcsrv/claim.go`) sets `want_playbook_texts=true`.
**Produces:** stats keys `playbook_texts: map[string]string` and `playbook_texts_left_out: map[string]string` (path → reason), exported as `worker.PlaybookTextsStatsKey`, `worker.PlaybookTextsLeftOutStatsKey`; `varsource.MaxPlaybookTextsBytes`, `varsource.TextTooLarge|TextBudget|TextNotUTF8|TextUnreadable` (string values `too_large`, `budget`, `not_utf8`, `unreadable`).
- [ ] Tests first (`sync_texts_test.go`): `readPlaybookTexts(fsys, paths)` returns texts in the given order; a 512 KiB + 1 file → `too_large`; files that together pass 4 MiB → the first ones kept, the rest `budget` (a later small file still fits if it fits — say which you chose and test it; *ruling: keep filling, a later file that fits is kept*); invalid UTF-8 → `not_utf8`; a symlink out of the subpath and a path under `.git` → `unreadable` (use the existing `openCloneFS` rules); an executor test: job without the flag → result stats have neither key; with the flag → both keys, paths = the report's playbook paths.
- [ ] Claim test: a claimed `sync_repo` job carries `want_playbook_texts=true`, a `run` job false.
- [ ] Implement, regen proto, lint, commit `feat(worker): ship playbook texts with a repo sync when the manager asks`.

## Task 2: Manager — store texts, serve them (spec (a) Storage, API)
**Files:** migration `00117_repo_playbook_texts.sql` (`ALTER TABLE repo_playbook_sources ADD COLUMN content text, ADD COLUMN content_left_out text`); `internal/db/queries/var_sources.sql` (insert takes both; new `GetRepoPlaybookText` by org, repo, path joined with `repo_var_sources` for commit/synced_at); `internal/jobs/finish_sources.go` (take the two keys out next to `takeVarSources`, pass them into `RecordVarSources`); `internal/repos/sources.go` + store; `internal/playbooks` service method `RepoText(ctx, orgID, playbookID) (RepoText, error)`; `internal/api` route `GET /orgs/{org}/playbooks/{id}/content`; backup fixture; OpenAPI/schema regen.
**Consumes:** Task 1's stats keys and reasons. **Produces:** `GET /api/v1/orgs/{org}/playbooks/{id}/content` → `{path, commit, synced_at, content?, left_out?}` with `left_out` one of `too_large|budget|not_utf8|unreadable|not_sent`; editor playbook → 422; no sync record → 404; permission `playbooks.read`.
- [ ] Tests: finish of a sync with both keys → neither key in the stored `jobs.stats`, also when the job failed/cancelled; texts stored per path with reasons; a path in the report but absent from both maps → `content` NULL, served as `not_sent`; a Failed report keeps the previous rows' texts; a sync without the keys (older worker) stores NULL; the payload over the limits (a forged text > 512 KiB, or totals > 4 MiB) is refused by the manager as `too_large`/`budget`, not stored; route: 200 shape, 422 editor, 404 no sync, cross-org 404, viewer ok, no membership as the other playbook routes.
- [ ] Implement, regen, backup fixture, lint, commit `feat(repos,playbooks,api): store repository playbook texts and serve them`.

## Task 3: Server — playbook revision and If-Match (spec (b))
**Files:** migration `00118_playbooks_revision.sql`; `internal/db/queries/playbooks.sql` (`UpdatePlaybookInline` takes an optional expected revision: `AND (sqlc.narg(expected_revision)::bigint IS NULL OR revision = sqlc.narg(expected_revision))`, sets `revision = revision + 1`, returns the row); `internal/playbooks/service.go` (`UpdateEditor` gains `expectedRevision *int64`, returns `ErrRevisionMismatch` when the row exists but the revision differs — reuse the package-level sentinel pattern of templates); `internal/api/playbook_handlers.go` (GET and PUT answer `ETag`, PUT reads optional `If-Match`, 412 on mismatch with the current revision in the message, as `template_variables_handlers.go`); DTO `revision`; OpenAPI/schema regen; backup fixture.
**Produces:** playbook DTO `revision: number`; ETag format identical to the templates' (read `template_variables_handlers.go` and reuse its helper).
- [ ] Tests: revision starts at 1 and increments per save; `If-Match` current → 200 + new ETag; stale → 412, text and revision unchanged; absent → saved; malformed `If-Match` → as the templates answer it; a repo playbook PUT still 422.
- [ ] Implement, regen, lint, commit `feat(playbooks,api): refuse a playbook save over a newer revision`.

## Task 4: SPA — repository text view and the conflict notice (spec (a) SPA, (b) SPA)
**Files:** `web/src/features/playbooks/` — `usePlaybookRepoText.ts` (query on the Task 2 route), `PlaybookEditorTab.tsx` (repo branch: read-only `CodeEditor` + "As of the sync on <time>, commit <short sha>" line with the repo link, Check and Outline from the text, Variables from the stored sources of this path (the `GET /repos/{id}/playbooks` entry, as the template page reads them); without text the existing note plus the reason), `usePlaybookDraft.ts` (base revision beside the base text; a clean draft follows a newer text and its revision), the save mutation (sends `If-Match: <ETag of the base revision>`), `PlaybookSaveConflict.tsx` (notice with **Show their changes** via `ui/diff/UnifiedDiff`, **Reload**, **Save mine anyway**); i18n en/de.
**Consumes:** Task 2 route; Task 3 `revision` / ETag / 412.
- [ ] Tests: repo playbook with text → read-only editor (typing does not change it), the sync line, Check/Outline fed with the text and Variables from the stored sources, no Save button; each `left_out` reason renders its sentence, `not_sent` says to sync again with a current worker; editor save sends `If-Match`; a 412 keeps the draft and shows the notice; Show their changes renders a diff of base vs. current; Reload replaces the draft with the current text and revision; Save mine anyway saves with the current revision; a clean draft adopts a newer text and revision.
- [ ] Commit `feat(web): show repository playbook text, refuse silent overwrites`.

## Task 5: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, full Playwright e2e (add one spec if feasible without a worker: open an editor playbook, save it through the API meanwhile, save in the page → the conflict notice), docs on the docs scratch branch (playbooks page: repository text, save conflicts), `docs/upgrades.md` note if version skew needs one, backlog 3.127 done, merge to local main and redeploy the local stacks.
