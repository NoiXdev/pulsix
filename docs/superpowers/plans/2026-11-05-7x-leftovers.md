# 7.x Leftovers (7.64, 7.73, 7.76–7.80, 7.82) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Invitation tokens never reach the manager log; sidebar tag and SSH edits neither freeze fact tags nor overwrite a concurrent host edit; a job template can be run on one host from the host page; the host-view and account-administration review leftovers are closed; the runs host filter is proven to use its index under a generic plan.

**Architecture:** The invitation link moves its token into the URL fragment (the 7.74 reset-link pattern) and the chi request logger gets a redacting formatter. Tag and SSH writes bump `hosts.revision` through one shared `BumpHostRevision` query in the same transaction as the write, with `If-Match` optional (checked when sent). A template run gains an optional `host_id` that narrows the resolved scope to one host and intersects the Ansible limit with that host's name. Everything else is local fixes behind the existing layers.

**Tech Stack:** Go 1.27 (pgx v5, sqlc, huma v2, chi v5), PostgreSQL 18 + TimescaleDB, React/TypeScript SPA (TanStack Router/Query, Vitest), Playwright E2E with mailpit.

**Spec:** Backlog rows in `docs/BACKLOG.md` (7.64, 7.73, 7.76–7.80, 7.82) and the Rulings section below.

## Global Constraints

- Everything in the repository is English; user-visible SPA strings in English and German (`web/src/i18n/en|de/*.ts`).
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal change. A "verify only" step runs the named command and records its output in the task report instead of changing code.
- Tests per package with a timeout (`go test -race -count=1 -timeout 900s ./internal/<pkg>/`; `internal/api` needs `-timeout 1800s`). Never one whole-module `go test ./...`. Full suite: `make test`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`; in `web/`: `npx vitest run <file>`, `npx eslint <files>`, `npx tsc -b`; OpenAPI types via `npm run api:gen` after every API shape change (Tasks 4, 6, 11). No prettier.
- E2E: in `web/`, `npx playwright test e2e/<spec>.ts` (the stack uses mailpit, `web/e2e/mailpit.ts`).
- sqlc is not installed as a binary: `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`, then `... diff` must be clean.
- No migration is expected. If Task 20 needs an index, it is a new additive migration file with a working Down; never edit a released one.
- Database tests use `internal/db/testdb`. Only touch Docker containers the tests start themselves.
- File splits are pure moves: no behaviour change, tests unchanged except moved.
- Backlog rows and the end-user docs in `NoiXdev/noix-docs` are updated by the controller after the branch (7.81 included).

## Review Focus

1. No request log line ever carries an invitation token: not for a new `/invite#token=…` link, not for an old `/invite/{token}` link, not with a query string appended (Task 2 test).
2. A tag added or removed in the sidebar sends only manual tags; a fact tag stays a fact tag (Task 3 test).
3. `PUT /hosts/{id}/tags` and `PUT /hosts/{id}/ssh` without `If-Match` still succeed for API-token clients; with a stale `If-Match` nothing is written; either write makes a host dialog opened before it answer 412 (Task 4 tests).
4. A template run narrowed to one host creates jobs whose `host_ids` is exactly that host, ships a limit that cannot match any other host, and is refused (422) for a host outside the template's scope or limit (Task 6 tests).
5. Reactivating a deactivated account whose invitation is pending leaves the invitation pending and gives the account no password (Task 11 test).

## Rulings

Binding decisions of the controller for this package:

1. **7.80:** New invitation links use the fragment form `{PublicURL}/invite#token=…` (same pattern as the 7.74 reset link: `ResetPasswordPage.tsx` reads the fragment and strips it with `history.replaceState`). Old links already mailed (`/invite/{token}`) keep working: the SPA route still accepts them and immediately rewrites the URL to the fragment form. The request logger never records an invitation token: it redacts the token segment of the SPA path `/invite/{token}` and of every API route that carries an invite token in its path; a redacted path reads `/invite/[redacted]`. An API route that takes the token in its path keeps it (clients may use it) and is redacted in the log. Tested at the logger level and, for the link format, in the tenancy service test. The invite mail template is checked too. *(Finding: no API route carries an invite token in its path — `POST /api/v1/invites/accept` takes it in the body, `DELETE /orgs/{org}/invites/{inviteId}` takes the invitation id — and the mail text is the `fmt.Sprintf` in `tenancy.Service.Invite`. Only the SPA path is redacted; the redaction test pins that the two API paths need none.)*
2. **7.78:** `HostTagsEditor` sends only the manual tags, exactly as the host dialog does; one SPA helper does the tag→manual conversion for both. *(7.77 item 3 is the Go-side conversion and gets its own helper in Task 8; the two do not share code.)*
3. **7.79:** Tag and SSH writes bump `hosts.revision` and honour `If-Match` the way `PATCH /hosts/{id}` checks it. `PATCH` *requires* it (428 when missing, `requireIfMatch`); these two routes never took it, so it stays **optional** there (`optionalIfMatch`, the inventory-member pattern): checked when sent, the write is unconditional when not, and the revision is bumped either way. The SPA callers (sidebar tag editor, SSH card) send `If-Match` and handle 412 the way the host dialog does. No migration.
4. **7.76:** "Run template…" beside "Run command" on the host page sidebar and in the host list row menu, for a managed host with `jobs.run`; it picks a job template and starts it with this host as the only target. Today `POST /api/v1/orgs/{org}/job-templates/{id}/run` (hook `useRunTemplateById`) takes no body and cannot narrow; it gets an additive optional body field `host_id`.
5. **7.77:** All items, including the re-review additions (invalidate `['host', org]` on agent revoke; the run dialog's empty flash). Item 7 is a docs line in `docs/upgrades.md`. Item 12: the metrics API (`GET /hosts/{id}/metrics`) takes exactly one check/item/metric per request, so the State sparklines poll every **60 s** instead of being merged into one request.
6. **7.82:** Everything except item (4), which stays parked in the backlog. Item (5): reactivation of a deactivated account with a pending invitation is allowed and leaves the invitation pending. Item (6): key the `clientip` limiters on the /64 for IPv6. Item (7): the member count query excludes deactivated accounts (a one-line query change), and the page hint stops saying they are included. The E2E items go into `web/e2e/accounts.spec.ts`.
7. **7.64:** All five items. Item (1) changes the compose README in this repo; the controller updates noix-docs `admin/operations.md` with the same snippet. Item (2): no release exists yet; wording that needs no referent. Item (3): reproduce with `-count=20 -race -timeout 900s ./internal/backup/`; if it does not reproduce, record the run and close without a code change.
8. **7.73:** A DB test under `internal/jobs` (testdb) seeds 50 000 jobs across 200 hosts, sets `plan_cache_mode = force_generic_plan`, runs `EXPLAIN (FORMAT JSON)` on the exact runs-list query with a host filter and asserts `jobs_host_ids_idx` is used; if not, fix the query (or add an additive index) until it is. Skipped under `-short`.

---

### Task 1: The invite page takes its token from the fragment; old links are rewritten (7.80)

**Files:** Modify `web/src/router.tsx` (`inviteRoute`, ~line 95), `web/src/features/members/InviteAcceptPage.tsx` (token source, ~line 35). Tests `web/src/features/members/InviteAcceptPage.test.tsx`, `web/src/router.test.tsx`.

**Interfaces:** `inviteRoute` becomes path `/invite` (component `InviteAcceptPage`); a new `legacyInviteRoute` at `/invite/$token` has no component and a `beforeLoad` that `throw redirect({ to: '/invite', hash: \`token=${params.token}\`, replace: true })`. `InviteAcceptPage` reads the token once with a `tokenFromFragment(window.location.hash)` helper (same shape as `ResetPasswordPage.tsx`'s; move that function to `web/src/lib/fragment.ts` and import it in both) and strips the fragment in a mount effect with `window.history.replaceState(window.history.state, '', pathname + search)`. No token → the existing invalid-token state (the 404 copy the page shows today for an unknown token).

- [ ] **Step 1: Failing tests:**
  - `InviteAcceptPage.test.tsx`: switch `renderPage` to the `/invite` route with `window.history.replaceState(null, '', '/invite#token=tok_abc123')` before render (as `ResetPasswordPage.test.tsx` does). Existing cases keep asserting the posted body `{ token: 'tok_abc123', … }`. New: `takes the token out of the address bar once it has read it` — after render `window.location.hash === ''` and `window.location.href` does not contain `tok_abc123`; `without a token the page says the link is invalid and posts nothing` — no request to `/api/v1/invites/accept`.
  - `router.test.tsx`: `describe('an invitation link mailed before the token moved into the fragment')` — `router.navigate({ to: '/invite/tok_old' })`, `await router.load()`; `router.state.location.pathname === '/invite'` and `router.state.location.hash === 'token=tok_old'`.
- [ ] **Step 2:** `npx vitest run src/features/members/InviteAcceptPage.test.tsx src/router.test.tsx src/features/auth/ResetPasswordPage.test.tsx` — expect the new cases to fail.
- [ ] **Step 3:** implement as above; update the router comment block next to the reset routes to cover the invitation link too.
- [ ] **Step 4:** the three test files pass; `npx eslint` on the touched files; `npx tsc -b`.
- [ ] **Step 5: Commit** `feat(web): read the invitation token from the URL fragment and rewrite old links`.

### Task 2: Mail the fragment link and keep invitation tokens out of the request log (7.80)

**Files:** Modify `internal/tenancy/service.go` (`Invite`, ~line 386: `link := s.publicURL + "/invite#token=" + token`), `internal/api/api.go` (~line 394: replace `middleware.Logger`), create `internal/api/request_log.go`. Tests `internal/tenancy/service_test.go` (~813 and `TestInviteCreatesUserSendsMailAndAcceptJoins` ~433), create `internal/api/request_log_internal_test.go`; update the token extractors in `internal/api/member_handlers_test.go` (~46, `extractInviteToken` ~284), `internal/api/audit_handlers_test.go` (~470), `internal/api/invite_mail_test.go` (~40-44), `web/e2e/mailpit.ts` (`extractInviteLink`, ~76) and the comment in `web/e2e/members.spec.ts` (~41).

**Interfaces:** in `internal/api/request_log.go`: `func redactRequestURI(uri string) string` — any path whose first segment is `invite` followed by a further segment becomes `/invite/[redacted]` (query string dropped with it, since a token could ride there too); everything else is returned unchanged. `func requestLogger(out io.Writer) func(http.Handler) http.Handler` — `middleware.RequestLogger(redactingFormatter{inner: &middleware.DefaultLogFormatter{Logger: log.New(out, "", log.LstdFlags)}})`, where `redactingFormatter.NewLogEntry(r)` clones the request (`r.Clone`), sets `RequestURI` and `URL.Path` to the redacted values on the clone only, and delegates. `api.New` mounts `requestLogger(os.Stdout)` in place of `middleware.Logger` (same output as before for every other path).

- [ ] **Step 1: Failing tests:**
  - tenancy: `TestInviteWithoutMailConfiguredReturnsTheLink` expects `AcceptURL == testPublicURL+"/invite#token="+inv.Token`; `TestInviteCreatesUserSendsMailAndAcceptJoins` additionally asserts the mail body contains `"Accept: "+testPublicURL+"/invite#token="+inv.Token` and not `"/invite/"`.
  - `TestRedactRequestURI` table: `/invite/tok_abc` → `/invite/[redacted]`; `/invite/tok_abc?x=1` → `/invite/[redacted]`; `/invite` → `/invite`; `/invite?x=1` → unchanged; `/api/v1/invites/accept` → unchanged; `/api/v1/orgs/acme/invites/0190…` → unchanged; `/o/acme/hosts` → unchanged; `/invitex/abc` → unchanged.
  - `TestRequestLoggerNeverRecordsAnInviteToken`: serve `GET /invite/tok_secret_123?a=b` through `requestLogger(&buf)` wrapping a 200 handler; `buf` contains `/invite/[redacted]` and not `tok_secret_123`; the handler itself still saw the original path (it records `r.URL.Path == "/invite/tok_secret_123"`).
  - The API test extractors now cut after `/invite#token=`; `mailpit.ts` splits on `/invite#token=`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/tenancy/` and `go test -race -count=1 -timeout 1800s -run 'TestRedact|TestRequestLogger|Invite' ./internal/api/` — expect failures.
- [ ] **Step 3:** implement; the `Invite` doc comment says the token rides in the fragment so no request log sees it.
- [ ] **Step 4:** `./internal/tenancy/` and `./internal/api/` pass; `npx playwright test e2e/members.spec.ts` passes (the invitee follows the new link; Task 1 made the page read it).
- [ ] **Step 5: Commit** `fix(tenancy,api): mail the invitation token in the URL fragment and redact it from the request log`.

### Task 3: The sidebar tag editor sends only manual tags (7.78)

**Files:** Create `web/src/features/hosts/tags.ts` (+ `tags.test.ts`). Modify `web/src/features/hosts/HostTagsEditor.tsx` (`current`, ~line 19), `web/src/features/hosts/HostDialog.tsx` (`manualTags`, ~line 105). Test `web/src/features/hosts/HostSidebar.test.tsx`.

**Interfaces:** `export function manualTagPairs(tags: readonly Tag[] | null | undefined): ParsedTag[]` — keeps `source === 'manual'`, maps to `{ key, value }`. Used by both callers; nothing else converts tags to manual pairs in the SPA.

- [ ] **Step 1: Failing tests:** `tags.test.ts` — mixed manual/fact input returns only the manual pairs, without `source`; `null` → `[]`. `HostSidebar.test.tsx` new case `sends only the manual tags when a tag is added or removed`: GET tags returns `env=prod` (manual) and `os=debian` (fact); adding `team=ops` PUTs exactly `[{ key: 'env', value: 'prod' }, { key: 'team', value: 'ops' }]`; removing `env` PUTs `[]` (the `os` fact tag is in neither body).
- [ ] **Step 2:** `npx vitest run src/features/hosts/tags.test.ts src/features/hosts/HostSidebar.test.tsx` — expect failure.
- [ ] **Step 3:** implement; `HostTagsEditor`'s `current()` becomes `manualTagPairs(tags.data?.tags)`; update its doc comment ("sends the manual tags only; a fact tag follows the facts").
- [ ] **Step 4:** both files and `HostDialog.test.tsx` pass; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): keep automatic tags out of the sidebar tag editor's writes`.

### Task 4: Tag and SSH writes bump and check the host revision (7.79, backend)

**Files:** Modify `internal/db/queries/hosts.sql` (new `BumpHostRevision`, + regenerated `internal/db/gen`), `internal/hosts/tags.go` (`SetTags`), `internal/hosts/tags_store_sql.go` (`ReplaceManualTags`), `internal/hosts/service.go` (Store interface ~199), `internal/sshconf/service.go` (`HostSSHInput`, `HostSSH`, `SetHostSSH`, Store `PutHostSSH`, new sentinel), `internal/sshconf/store_sql.go` (`PutHostSSH`), `internal/api/tag_handlers.go` (PUT tags), `internal/api/sshconf_handlers.go` (PUT ssh, `mapSSHErr`), fakes in `internal/hosts/*_test.go`, `internal/sshconf/*_test.go`, `web/src/api/schema.d.ts` (regenerated). Tests `internal/hosts/tags_store_sql_test.go`, `internal/hosts/tags_test.go`, `internal/sshconf/store_sql_test.go`, `internal/sshconf/service_test.go`, `internal/api/tag_handlers_test.go`, `internal/api/sshconf_handlers_test.go`.

**Interfaces:**
- Query `-- name: BumpHostRevision :one`: `UPDATE hosts SET revision = revision + 1, updated_at = now() WHERE id = @id AND org_id = @org_id AND (sqlc.narg(if_revision)::bigint IS NULL OR revision = sqlc.narg(if_revision)::bigint) RETURNING revision;` No row → the store reads the host (as `UpdateHostWith` does) to tell `ErrNotFound` from a stale revision.
- `hosts.Service.SetTags(ctx, orgID, hostID uuid.UUID, tags []Tag, ifRevision *int64) (revision int64, err error)`; `Store.ReplaceManualTags(ctx, orgID, hostID uuid.UUID, tags []Tag, ifRevision *int64) (int64, error)` bumps first, then replaces the manual tags, in one `db.WithOrg` transaction; stale → `hosts.ErrRevisionMismatch`.
- `sshconf.HostSSHInput.IfRevision *int64`; `sshconf.HostSSH.HostRevision int64` (set by a write, zero on a read); `Store.PutHostSSH(ctx, h HostSSH, ifRevision *int64) (HostSSH, error)` bumps (shared `BumpHostRevision`) and upserts in one `db.WithOrg` transaction; `sshconf.ErrRevisionMismatch` → `mapSSHErr` 412 with the host route's text ("the host changed since you loaded it; reload and try again").
- API: `hostTagsSetInput.IfMatch string \`header:"If-Match" doc:"The host's ETag; optional — when sent, a stale one answers 412"\``, parsed with `optionalIfMatch`; output gains `ETag string \`header:"ETag"\`` and `Body.HostRevision int64 \`json:"host_revision"\``. PUT ssh likewise, with a new `hostSSHPutOutput` (GET keeps `hostSSHOutput` without the field).

- [ ] **Step 1: Failing tests:**
  - DB (hosts): `TestReplaceManualTagsBumpsTheRevision` (nil `ifRevision`: revision r → r+1, returned value r+1); `TestReplaceManualTagsRefusesAStaleRevision` (`ifRevision` r-1 → `ErrRevisionMismatch`, manual tags and revision unchanged); unknown host → `ErrNotFound`.
  - DB (sshconf): `TestPutHostSSHBumpsTheHostRevision`, `TestPutHostSSHRefusesAStaleRevision` (settings and revision unchanged).
  - API `tag_handlers_test.go` `TestSetHostTagsHonoursIfMatch`: no header → 200, `ETag` is `"r+1"` and `host_revision` r+1; current `If-Match` → 200; stale → 412 and GET tags unchanged; malformed (`7` unquoted) → 412. `TestHostEditAfterATagWriteAnswers412`: GET host (ETag r), PUT tags without `If-Match`, then PATCH host with `If-Match: "r"` → 412.
  - API `sshconf_handlers_test.go`: the same two tests for PUT ssh (`TestSetHostSSHHonoursIfMatch`, `TestHostEditAfterAnSSHWriteAnswers412`); an API-token client without `If-Match` still gets 200 (Review Focus 3).
- [ ] **Step 2:** run the four packages, expect failures (compile errors on the new signatures count).
- [ ] **Step 3:** implement; regenerate sqlc; `npm run api:gen`.
- [ ] **Step 4:** `./internal/hosts/`, `./internal/sshconf/`, `./internal/api/` pass; sqlc diff clean; `npx tsc -b` passes (no SPA caller changes yet).
- [ ] **Step 5: Commit** `fix(hosts,sshconf,api): cover tag and SSH writes by the host revision`.

### Task 5: The sidebar tag editor and the SSH card send If-Match and handle 412 (7.79, SPA)

**Files:** Create `web/src/features/hosts/StaleNotice.tsx`. Modify `web/src/api/hooks/useTags.ts` (`useSetHostTags`), `web/src/api/hooks/useSsh.ts` (`useSetHostSSH`), `web/src/features/hosts/HostTagsEditor.tsx`, `web/src/features/hosts/HostSidebar.tsx` (~line 50, pass `revision`), `web/src/features/hosts/HostSshCard.tsx` (`HostSshCard`/`HostSshForm` props, `submit` ~line 92), `web/src/features/hosts/HostDetailPage.tsx` (~line 110, pass `revision`), `web/src/features/hosts/HostDialog.tsx` (use `StaleNotice`, ~lines 253 and 377). Tests `web/src/features/hosts/HostSidebar.test.tsx`, `web/src/features/hosts/HostSshCard.test.tsx`, `web/src/features/hosts/HostDialog.test.tsx` (unchanged behaviour).

**Interfaces:**
- `useSetHostTags(org, hostId)` → `mutationFn: ({ tags, revision }: { tags: Array<Pick<Tag, 'key' | 'value'>>; revision: number })` (the API layer keeps its own type; it never imports from `features`), header `ifMatch(revision)` (export the existing `ifMatch` from `useHosts.ts`); on success writes `res.host_revision` into the cached `['host', org, hostId]` (`setQueryData`, only when cached) and invalidates as today plus `['host', org, hostId]`.
- `useSetHostSSH(org, hostId)` → `mutationFn: ({ body, revision }: { body: HostSSHInput; revision: number })`, same success handling.
- `StaleNotice({ onReload }: { onReload: () => Promise<unknown> })` — the dialog's existing block: `t.hosts.dialog.stale`, a Reload button (disabled while reloading), and `t.hosts.dialog.reloadFailed` when the reload throws. `HostDialog` uses it instead of its inline copy.
- `HostTagsEditor` gains `revision: number`; `HostSshCard` gains `revision: number`. On a 412 each shows `StaleNotice` with `useReloadHost(org, hostId)`; any other error keeps today's handling.

- [ ] **Step 1: Failing tests:** `HostSidebar.test.tsx` — `sends the host's revision as If-Match with a tag change` (PUT carries `If-Match: "3"` for a host at revision 3); `a 412 on a tag change offers Reload, which refetches the host and its tags` (after Reload, GET host and GET tags were requested again and the notice is gone). `HostSshCard.test.tsx` — the same two for Save. `HostDialog.test.tsx` existing 412 cases stay green.
- [ ] **Step 2:** run the three test files, expect failures.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the files pass; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): send the host revision with tag and SSH edits and offer a reload on 412`.

### Task 6: Start a template run narrowed to one host (7.76, backend)

**Files:** Modify `internal/jobs/run_request.go` (`RunRequest`, `EnqueueRunWith` ~line 106), `internal/jobs/service.go` (`resolveRunTargets` ~line 675, new sentinel next to `ErrEmptyScope` ~line 40), `internal/jobs/limit.go` (`narrowLimit`), `internal/api/job_handlers.go` (run route ~line 372, `mapJobsErr` ~line 602), `web/src/api/schema.d.ts` (regenerated). Tests `internal/jobs/run_request_test.go`, `internal/jobs/limit_test.go`, `internal/api/job_handlers_test.go`.

**Interfaces:**
- `RunRequest.OnlyHost *uuid.UUID` — "narrows the run to this host: it must be managed (else `ErrNotFound`) and among the template's resolved targets under the effective limit (else `ErrHostNotInScope`)".
- `var ErrHostNotInScope = errors.New("the host is not among the template's targets")`; `mapJobsErr` → 422 with that text.
- `func narrowLimit(effective, hostName string) string` — `hostName` when `effective` is empty, else `effective + ":&" + hostName` (Ansible's intersection).
- In `EnqueueRunWith`, when `OnlyHost` is set: `s.hosts.GetHost` (unmanaged → `ErrNotFound`); compute `gate := effectiveLimit(tmpl.Limit, req.Overrides)`; if `parseLimit(gate)` decides the host name does not match → `ErrHostNotInScope`; seal a copy of the overrides whose `Limit` is `narrowLimit(gate, host.Name)`; pass `gate` (not the narrowed limit) to `enqueueRunScope`. In `resolveRunTargets`, right after `Resolve`: keep only `*req.OnlyHost` (`onlyHost(targets, id) []ZoneTargets`, at most one zone with one host); none left → `ErrHostNotInScope`. The key, exclusion and pin guards then run as today.
- API: `templateRunInput { Org, ID string; Body *struct{ HostID string \`json:"host_id,omitempty" doc:"Run on this one host only; it must be among the template's targets"\` } }` — a pointer body, optional (add `required:"false"` if huma does not infer it; the no-body test pins it). `host_id` parsed with `parseUUID` (400 when malformed). Audit summary names the host when narrowed.

- [ ] **Step 1: Failing tests:**
  - `TestNarrowLimit` table: `("", "web-01")` → `web-01`; `("web*", "web-01")` → `web*:&web-01`.
  - `TestEnqueueRunWithOnlyHost` (fakes, one zone with hosts A and B in scope): one job, `HostIDs == [A]`, sealed overrides' `Limit` is `"a-name"`; template limit `web*` with A named `web-01` → limit `web*:&web-01`; host C outside the scope → `ErrHostNotInScope` and no job; A excluded by a decidable template limit (`db*`) → `ErrHostNotInScope`; unmanaged or unknown host → `ErrNotFound`; caller-supplied `Overrides.ExtraVars` survive into the sealed payload.
  - API: `TestRunRouteNarrowsToOneHost` — 202, one job whose `host_ids` is `[host]`; `TestRunRouteRefusesAHostOutsideTheScope` → 422; an unknown or unmanaged `host_id` → 404; malformed `host_id` → 400; `TestRunRouteCreatesGroupedJobs` (no body) stays green.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/jobs/` and `-run 'TestRunRoute' ./internal/api/` (1800 s) — expect failures.
- [ ] **Step 3:** implement; `npm run api:gen`.
- [ ] **Step 4:** `./internal/jobs/`, `./internal/api/` pass; tsc.
- [ ] **Step 5: Commit** `feat(jobs,api): start a job template run on one host`.

### Task 7: "Run template…" on the host page and in the host row menu (7.76, SPA)

**Files:** Create `web/src/features/runs/RunTemplateDialog.tsx` (+ `.test.tsx`). Modify `web/src/api/hooks/useTemplates.ts` (`useRunTemplateById`, ~line 57), `web/src/features/templates/TemplatesPage.tsx` (~line 71 caller), `web/src/features/hosts/useHostActions.tsx` (dialog state, `runTemplate`), `web/src/features/hosts/HostSidebar.tsx` (button beside Run command, ~line 55), `web/src/features/hosts/HostRow.tsx` (~line 55), `web/src/i18n/en|de/hosts.ts`, `web/src/i18n/en|de/runs.ts`. Tests `web/src/features/hosts/HostSidebar.test.tsx`, `web/src/features/hosts/HostsListPage.test.tsx`, `web/src/features/templates/TemplatesPage.test.tsx` (unchanged behaviour).

**Interfaces:** `useRunTemplateById(org)` → `mutationFn: ({ id, hostId }: { id: string; hostId?: string })`, body `{ host_id: hostId }` only when set. `RunTemplateDialog({ org, host, onClose }: { org: string; host: Host; onClose: () => void })` — `useTemplates(org)`, a `Select` of templates (empty list → `t.runs.templateRun.none`), Run starts the run with `hostId: host.id` and navigates to `/o/$org/runs/$id` of the first job (as `AdhocDialog` does); a refusal shows the server's message inline (422 not in scope, 422 not ready). `HostActions.runTemplate: () => void`, offered when `canRun` (managed + `jobs.run`). Strings: `t.hosts.detail.runTemplate` / `t.hosts.row.runTemplate` = "Run template…" / "Vorlage ausführen…", `t.runs.templateRun.{title(host), template, run, none, failed}`.

- [ ] **Step 1: Failing tests:** `RunTemplateDialog.test.tsx` — lists the templates; Run POSTs `/job-templates/{id}/run` with `{ host_id: 'h1' }` and navigates to the run; a 422 shows its detail and stays open; no templates → the empty text and a disabled Run. `HostSidebar.test.tsx` — an admin on a managed host sees "Run template…" next to "Run command"; not offered on an unmanaged host or to a viewer. `HostsListPage.test.tsx` — the row menu has "Run template…" for a managed host and opens the dialog.
- [ ] **Step 2:** run the files, expect failures.
- [ ] **Step 3:** implement; update the `TemplatesPage` caller to `{ id: tpl.id }`.
- [ ] **Step 4:** the files and `TemplatesPage.test.tsx` pass; eslint; tsc.
- [ ] **Step 5: Commit** `feat(web): run a job template on one host from the host page and the host list`.

### Task 8: 7.77 backend minors (items 3–7)

**Files:** `internal/hosts/tags.go`, `internal/hosts/host_write.go` (`validateJump` ~47, `extrasFrom` ~76, `writeExtras` FK branch), `internal/sshconf/service.go` (`SetHostSSH` mode checks ~288-293), `internal/api/host_handlers.go` (`toInput` ~125, PATCH/POST audit summaries, `mapHostsErr` ~423), `internal/api/tag_handlers.go` (~138), `docs/upgrades.md` (Migrations section). Tests `internal/hosts/tags_test.go`, `internal/hosts/host_write_test.go`, `internal/sshconf/service_test.go`, `internal/api/host_write_handlers_test.go`.

**Interfaces:**
- (3) `func ManualTags(tags []Tag) []Tag` in `internal/hosts/tags.go` — copies key and value, sets `TagSourceManual`; used by `SetTags`, `extrasFrom` and both API handlers (the handlers build `[]hosts.Tag` from the body with one `tagsFromBody([]tagInput) []hosts.Tag` in `tag_handlers.go` that calls it).
- (4) `func ValidateJump(mode string, bastionID *uuid.UUID) error` in `internal/hosts/host_write.go` returning plain errors (`errJumpMode`, `errJumpTarget`, no sentinel); `hosts` wraps them in `hosts.ErrInvalidInput`, `sshconf.SetHostSSH` in `sshconf.ErrInvalidInput` (sshconf already imports hosts). The texts stay as today.
- (5) `func hostUpdateSummary(name string, in hosts.HostInput) string` in `host_handlers.go`: "updated host web-01", plus "; set its jump host" when `in.Jump != nil` and "; set its tags" / "; set its jump host and tags".
- (6) `var ErrJumpHostNotFound = fmt.Errorf("%w: jump host", ErrNotFound)` returned by `writeExtras` on the FK violation; `mapHostsErr` checks it before `ErrNotFound` → 404 "jump host not found".
- (7) One paragraph in `docs/upgrades.md` → Migrations: migration 00079 builds `jobs_host_ids_idx` with a plain `CREATE INDEX`, which blocks writes to `jobs` (new runs, job status) while it builds; on a large run history expect the upgrade's migration step to take correspondingly longer, and upgrade outside busy automation windows.

- [ ] **Step 1: Failing tests:** `TestManualTagsMarksEveryTagManual` (a fact-sourced input comes back manual, input slice untouched); `TestValidateJump` table (inherit/none/explicit with and without id) and `TestSetHostSSHUsesTheSharedJumpRules` (same refusals, wrapped in `sshconf.ErrInvalidInput`); API `TestUpdateHostAuditSaysWhatChanged` (PATCH with tags → audit summary `updated host web-01; set its tags`; with jump → `…; set its jump host`; neither → exactly `updated host web-01`); `TestUpdateHostWithAnUnknownJumpHostNamesIt` (PATCH and POST with an explicit random bastion id → 404 detail `jump host not found`; PATCH on an unknown host id → detail `not found`).
- [ ] **Step 2:** run `./internal/hosts/`, `./internal/sshconf/`, `-run 'TestUpdateHost' ./internal/api/` — expect failures.
- [ ] **Step 3:** implement (3)–(6); write (7).
- [ ] **Step 4:** the three packages pass.
- [ ] **Step 5: Commit** `refactor(hosts,api): one manual-tag helper and one set of jump rules, and say what a host update changed` and `docs(upgrades): say that migration 00079 blocks job writes while it builds`.

### Task 9: 7.77 SPA minors in the shared UI (items 1, 2, 8, 14)

**Files:** `web/src/ui/switch.tsx`, `web/src/ui/list-row.tsx` (`ListRow`, ~line 47-58), `web/src/features/notifications/AlertRoutesPage.tsx` (~line 188). Tests `web/src/ui/switch.test.tsx`, `web/src/ui/list-row.test.tsx`, `web/src/features/notifications/AlertRoutesPage.test.tsx`.

**Interfaces:** (1) `SwitchProps` loses `name` (no caller passes it). (2) the route toggle becomes `<Switch checked={r.enabled} aria-label={…} onCheckedChange={() => void onToggle(r)} />`. (8) `ListRow`'s `hover:bg-card`/`focus-within:bg-card` become `hover:bg-background`/`focus-within:bg-background` (visible inside a `Card`). (14) the link's name comes from a visually hidden `<span id={nameId} className="sr-only">{label}</span>` via `aria-labelledby`, and the cells are its description via `aria-describedby={detailsId}` on a `<span id={detailsId} className="contents">` around `children` (ids from `useId`).

- [ ] **Step 1: Failing tests:** `switch.test.tsx` — `// @ts-expect-error name is not a Switch prop` on `<Switch name="x" … />` (fails `tsc -b` until removed). `list-row.test.tsx` — `getByRole('link', { name: 'web-01', description: /10\.0\.0\.1/ })` finds the row; the row container's class contains `hover:bg-background` and not `hover:bg-card`. `AlertRoutesPage.test.tsx` — the toggle is the shared Switch (`getByRole('switch', { name: … })` has `aria-checked` and clicking it PATCHes `enabled: false`; assert the `h-[19px]` size class of `ui/switch`).
- [ ] **Step 2:** run the files and `npx tsc -b`, expect failures.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the files pass, plus every test that queries rows by link name (`npx vitest run src/features` — the full SPA run, since `ListRow` is everywhere); eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): tidy the switch and list row primitives and use the shared switch for alert routes`.

### Task 10: 7.77 SPA minors on the host page (items 10–13, revoke invalidation, run dialog flash)

**Files:** `web/src/features/hosts/HostSidebar.tsx` (drop `useOrg`, take `role`), `web/src/features/hosts/HostDetailPage.tsx` (~line 89 pass `role`), `web/src/api/hooks/useChecks.ts` (`useHostMetric` ~204), `web/src/features/hosts/HostStateSection.tsx` (~61), heading levels in `ConnectionTestCard.tsx` (~31), `EffectiveRulesPanel.tsx` (~61, ~172), `HostAgentCard.tsx` (~38), `HostAlertRoutesCard.tsx` (~88), `HostSshCard.tsx` (title and "install key"), `web/src/api/hooks/useAgents.ts` (`useInvalidate` ~4), `web/src/api/hooks/useHosts.ts` (`useHosts` options), `web/src/features/runs/AdhocDialog.tsx` (~52). Tests `HostDialog.test.tsx`, `HostSidebar.test.tsx`, `HostStateSection.test.tsx`, `HostDetailPage.test.tsx`, new `web/src/api/hooks/useAgents.test.tsx`, `web/src/features/runs/AdhocDialog.test.tsx`.

**Interfaces:** `HostSidebar({ org, host, role, actions })`. `useHostMetric(org, hostId, selection, opts?: { refetchMs?: number })` (default 30 000; `HostStateSection` passes 60 000; `MetricGraphPanel` unchanged). Card titles inside a `Section` become `h3`, their sub-headings `h4`. `useAgents.ts` `useInvalidate` adds `['host', org]`. `useHosts(org, filter, opts?: { keepPrevious?: boolean })` sets `placeholderData: keepPreviousData` when asked; `AdhocDialog` asks.

- [ ] **Step 1: Failing tests:**
  - (10) `HostDialog.test.tsx`: `a non-412 save failure shows the server's message and keeps the dialog open` (PATCH 500 with detail → message visible, no stale notice); `a failed reload after a 412 says the reload failed` (412, then GET host 500 on Reload → `reloadFailed` text).
  - (11) `HostSidebar.test.tsx`: rendering with `role="viewer"` issues no `GET /api/v1/orgs/acme` request and shows read-only switches.
  - (12) `HostStateSection.test.tsx`: with fake timers, a tile's metric is fetched again after 60 s and not after 30 s.
  - (13) `HostDetailPage.test.tsx`: `getAllByRole('heading', { level: 2 })` names are exactly the section titles; "SSH access" (the card title) is level 3.
  - Revoke: `useAgents.test.tsx` — after the revoke mutation succeeds, `invalidateQueries` was called with `{ queryKey: ['host', 'acme'] }`.
  - Flash: `AdhocDialog.test.tsx` — type a search whose response is held back; while it is pending the previous host rows are still shown.
- [ ] **Step 2:** run the files, expect failures.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `npx vitest run src/features/hosts src/features/runs src/api/hooks`; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): host page leftovers — role from the page, slower sparklines, heading levels, fresh host after an agent revoke`.

### Task 11: 7.82 backend minors (items 1–3, 5–8)

**Files:** `internal/api/auth_rate_limit.go` (`withAuthRateLimit` doc ~33, new option), `internal/api/password_handlers.go` (~58), `internal/db/queries/auth.sql` (`MarkSessionMFAComplete` ~29, + gen), `internal/api/member_handlers.go` (accept route ~248), `internal/accounts/actions.go` (`Reactivate` ~133, `withAccount` ~172), `internal/db/queries/tenancy.sql` (`ListOrganizationOverviews` ~42, + gen), `web/src/i18n/en|de/instance.ts` (`orgs.hint`), `web/src/features/instance/OrganizationsPage.tsx` (doc comment), `internal/api/org_handlers.go` (`registerOrgRoutes` doc comment). Tests `internal/api/auth_rate_limit_test.go`, `internal/auth/last_sign_in_sql_test.go`, `internal/api/must_change_password_test.go` (`TestMustChangePasswordInviteAcceptNeedsTheChange` ~304), `internal/accounts/actions_test.go`, `internal/accounts/store_sql_test.go` or `internal/api/admin_user_handlers_test.go`, `internal/tenancy/store_sql_test.go` (`TestSQLStoreListAllOrgs` ~839), `internal/clientip/limiter_test.go`.

**Interfaces:**
- (1) `func withAuthRateLimitCharging(deps Deps, charged ...int) func(*huma.Operation)` — like `withAuthRateLimit`, but only the listed statuses keep their slot; `POST /auth/password` uses it with `http.StatusBadRequest` (wrong current password, expired temporary password), so a 403 `step_up_required` and a 422 are refunded. `withAuthRateLimit`'s doc comment lists every route it is on: login, TOTP verify, passkey login finish, invite accept, setup completion, password reset; and names the charging variant for the password change.
- (2) the `previous` CTE reads `… WHERE id = sqlc.arg(id) FOR UPDATE`, so a concurrent completion waits and then sees `mfa_complete = true` and stamps nothing.
- (3) when `AcceptInvite` returns `tenancy.ErrSignInRequired` and the caller holds an MFA-complete cookie session whose account must change its password, answer `refuseUntilPasswordChanged(p)` (403 `password_change_required`) instead of 409; the SPA's global handler already sends that session to `/account/password`.
- (5) `func (s *Service) withLockedAccount(ctx, target, fn)` without the pending check; `withAccount` = `withLockedAccount` + `ErrInvitePending`; `Reactivate` uses `withLockedAccount`. Its doc comment: a deactivated invitee is reactivated with the invitation still pending.
- (6) **already done, verify only:** `clientip.Limiter` keys IPv6 per /64 since `01cd5e9` (`key`, `limiter.go` ~165), and every limiter (auth, forgot, hooks, enrollment, agent install) is a `clientip.Limiter`.
- (7) `(SELECT count(*) FROM org_memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = o.id AND u.disabled_at IS NULL)::int AS member_count`; the en/de hint drops the "include deactivated accounts" sentence; the page doc comment likewise.
- (8) rewrap `registerOrgRoutes`' doc comment (the "its owner) and / listing …" break).
- Item (4) is parked: not touched.

- [ ] **Step 1: Failing tests:**
  - (1) `TestPasswordChangeLimiterChargesOnlyWrongPasswords` (AuthRateLimit 1): a 422 too-short attempt and a 403 stale-factor attempt leave the budget; then a wrong current password answers 400 and the next attempt 429.
  - (2) `TestMarkMFACompleteStampsOnceUnderConcurrency` (DB): tx A sets the pending session's `mfa_complete = true` and holds the row; `MarkMFAComplete` runs in a goroutine; wait until `pg_stat_activity` shows it waiting on a lock; clear `users.last_sign_in_at`; commit A; the goroutine returns; `last_sign_in_at` is still NULL.
  - (3) `TestMustChangePasswordInviteAcceptNeedsTheChange` expects 403 with detail `password_change_required` (membership count still 0).
  - (5) unit `TestReactivateAnAccountWhoseInvitationIsPending` (fake store with `HasPassword: false`, deactivated → nil error, actions `reactivate`, `revoke sessions`, `void reset tokens`); DB/API: after reactivation the admin user list shows `invite_pending` and the invitation is still accepted with a new password.
  - (6) verify: `go test -race -count=1 -run TestLimiterKeysIPv6ByPrefix ./internal/clientip/` passes; add `TestAuthRateLimitSharesTheBudgetOfAnIPv6Slash64` to `auth_rate_limit_test.go` using its `postFrom` helper with two `X-Forwarded-For` addresses of one /64 behind a trusted proxy (second refused at limit 1), a third from another /64 allowed.
  - (7) `TestSQLStoreListAllOrgs`: a member whose account is deactivated is not counted.
- [ ] **Step 2:** run `./internal/api/` (1800 s), `./internal/auth/`, `./internal/accounts/`, `./internal/tenancy/`, `./internal/clientip/` — expect failures (except the verify-only one).
- [ ] **Step 3:** implement; regenerate sqlc; `npm run api:gen` if the OpenAPI document moved (it should not).
- [ ] **Step 4:** the packages pass; sqlc diff clean; `npx vitest run src/features/instance/OrganizationsPage.test.tsx`.
- [ ] **Step 5: Commit** `fix(api,auth,accounts): 7.74 review leftovers — charge only wrong passwords, lock the factor stamp, reactivate invitees` and `fix(tenancy,web): count only active accounts as organization members`.

### Task 12: 7.82 backend test hygiene

**Files:** `internal/auth/password_test.go` (`TestCheckPasswordPolicy` ~103), `internal/api/password_handlers.go` + `internal/api/password_reset_handlers.go` (too-short detail), `internal/api/password_handlers_test.go` (~126 and the token subtest ~147), `internal/accounts/fake_store_test.go` (~199), `internal/accounts/reset_test.go` (`TestRequestResetWorkHasADeadline` ~245), `internal/api/password_reset_handlers_test.go`, `internal/api/hook_handlers_test.go` (`TestHookOnActiveSkipAndTimestampWindow` ~205).

**Interfaces:** const `passwordTooShort = "password_too_short"` next to the other refusal details in `password_handlers.go`; `mapAccountsErr` and the reset handler (~118) answer 422 with it for `errors.Is(err, auth.ErrPasswordTooShort)`, keeping 422 with the error text for any other `ErrInvalidInput`. (The SPA uses the code in Task 13.)

- [ ] **Step 1: Failing tests / rewrites:**
  - `TestCheckPasswordPolicy` iterates a slice of named cases with `t.Run`.
  - `password_handlers_test` "too short" expects detail `password_too_short`; the reset handler test likewise. The "an API token" subtest is removed with a comment pointing at `must_change_gate_internal_test.go` (it got 401 because the route resolves no bearer at all, not from the gate).
  - The fake records the deadline (`emailDeadlineAt time.Time`); `TestRequestResetWorkHasADeadline` asserts it lies within `[start+ResetJobTimeout-5s, start+ResetJobTimeout+1s]`.
  - New `TestForgotPasswordStillAnswers202WhenTheRequestIsDropped`: mail configured, `Shutdown` called on `s.Deps.Accounts` (type-assert to `interface{ Shutdown(context.Context) error }`), then `POST /api/v1/auth/password/forgot` → 202 with the usual body and no mail sent.
  - `TestHookOnActiveSkipAndTimestampWindow`: reproduce with `go test -race -count=20 -timeout 1800s -run TestHookOnActiveSkipAndTimestampWindow ./internal/api/`; if it fails, fix the cause and keep the output in the report; if not, record the run and leave the test alone.
- [ ] **Step 2:** run, expect the detail and deadline tests to fail.
- [ ] **Step 3:** implement the detail code.
- [ ] **Step 4:** `./internal/auth/`, `./internal/accounts/`, `./internal/api/` pass.
- [ ] **Step 5: Commit** `test(auth,accounts,api): tighten the 7.74 tests and name the too-short refusal`.

### Task 13: 7.82 SPA minors in the sign-in and account flows (items 12, 14, 15, 16)

**Files:** `web/src/features/users/useAccountActionRunner.ts`, `web/src/features/users/AccountActions.tsx`, `web/src/features/account/PasswordChangeForm.tsx` (`refused` ~75), `web/src/features/auth/ResetPasswordPage.tsx` (~65), `web/src/api/hooks/useMe.ts`, `web/src/routes/RootRedirect.tsx`, `web/src/routes/EnrolmentLayout.tsx` (~60), `web/src/features/account/ChangePasswordPage.tsx` (~44), `web/src/lib/session.ts`, i18n `users.ts`. Tests `AccountActions.test.tsx`, `PasswordChangeForm.test.tsx`, `ResetPasswordPage.test.tsx`, `RootRedirect.test.tsx`, `EnrolmentLayout.test.tsx`, `ChangePasswordPage.test.tsx`, `web/src/lib/session.test.tsx`.

**Interfaces:**
- (12) `useAccountActionRunner` blocks leaving the page while `running` (TanStack `useBlocker({ shouldBlockFn: () => running !== null && !window.confirm(t.users.actions.leaveWhileRunning), enableBeforeUnload: () => running !== null })`, the FormPage pattern), so a temporary password is never lost to an unmount; `AccountActions` renders `<p role="status">{t.users.actions.running(label)}</p>` while `running` and the confirm dialog is closed.
- (14) `PasswordChangeForm.refused` and `ResetPasswordPage` treat a 422 as too short only when its detail is `password_too_short` (Task 12); any other 422 shows the server's message.
- (15) `useMe` stays as is; `export function useSettledMe(opts)` returns the query with `data: undefined` while a refetch of stale data is in flight (`isFetching && isStale`), so `RootRedirect`, `EnrolmentLayout` and `ChangePasswordPage` redirect only on a fresh answer and show loading meanwhile.
- (16) `export function useSignOut(): () => Promise<void>` in `web/src/lib/session.ts` (logout, navigate to `/login`, reset the session cache); both buttons use it.

- [ ] **Step 1: Failing tests:** (12) `AccountActions.test.tsx` — while a held-back temporary-password request runs and the dialog was closed, a status line names the action; navigating away asks to confirm and, when declined, the reveal still appears when the answer lands. (14) `PasswordChangeForm.test.tsx` — a 422 `password_too_short` shows the length rule; a 422 with another detail shows that detail. `ResetPasswordPage.test.tsx` likewise. (15) `RootRedirect.test.tsx` — a cached stale `me` with `must_change_password: true` while `/auth/me` now answers `false`: the router never visits `/account/password` and ends on the org's hosts page. (16) `session.test.tsx` — `useSignOut` calls `POST /auth/logout`, lands on `/login` and clears the cache; the two page tests still find their Sign out buttons working.
- [ ] **Step 2:** run the files, expect failures.
- [ ] **Step 3:** implement; en/de strings `users.actions.running(action)`, `users.actions.leaveWhileRunning`.
- [ ] **Step 4:** the files pass; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): account flows — keep a running action's answer, read only real length refusals as such, redirect on a fresh session`.

### Task 14: 7.82 SPA minors on the settings and organization pages (items 9, 10, 11, 13, 17, 18)

**Files:** `web/src/api/hooks/useOrgs.ts` (`useUpdateOrg` ~25), `web/src/i18n/en/settings.ts` (~36 `saveFailed`), `web/src/i18n/en/language.ts` (~14, ~26), `web/src/features/instance/shared/DefinitionLoader.tsx` + the three `Shared*FormPage.tsx`, `web/src/features/members/MembersPage.tsx` (invite form ~145, role select ~175), `web/src/features/members/MemberPage.tsx` (~157), new `web/src/features/members/roles.ts`, i18n `members.ts`, `web/src/features/instance/OrganizationsPage.tsx`, `web/src/ui/list-row.tsx`. Tests `OrgGeneralPage.test.tsx`, `SharedRepoFormPage.test.tsx` (+ the template and check form tests), `MembersPage.test.tsx`, `MemberPage.test.tsx`, `OrganizationsPage.test.tsx`, `list-row.test.tsx`.

**Interfaces:**
- (9) `useUpdateOrg`'s `onSuccess` returns `Promise.all` of its two invalidations, so `mutateAsync` resolves after the fresh name is in the cache.
- (10) en `saveFailed` strings of `settings.ts` and `language.ts` start with a capital letter.
- (11) `export function CreateGate({ children }: { children: ReactNode })` in `DefinitionLoader.tsx`: `useMe()` pending → `QueryLoading`; not an instance admin → `<QueryError error={new ApiError(403, null)} />`; else children. The form pages render `id ? <DefinitionLoader …> : <CreateGate>{form(undefined)}</CreateGate>`, so the create page no longer loads the list.
- (13) `roleOptions(t, roles)` in `roles.ts` returns `{ value, label: t.members.roles[value] }`; en Viewer/Operator/Admin/Owner, de Betrachter/Operator/Admin/Inhaber; used by both selects.
- (17) the invite `<form noValidate>`.
- (18) the loading branch is reachable (`useQueryGuard` returns null while pending) — **verify only**, keep it; the non-member row becomes `export function StaticListRow({ columns, title, children })` in `ui/list-row.tsx`, sharing `ListRow`'s row classes through one constant.

- [ ] **Step 1: Failing tests:** (9) `OrgGeneralPage.test.tsx` — with the refetched org answering the new name after a delay, the name field never shows the old name after Save (assert on every render via a `MutationObserver` or by checking the value immediately after the save resolves). (11) `SharedRepoFormPage.test.tsx` — the create page shows the empty form when `GET …/definitions` answers 500 and makes no request to it; a non-admin sees the access-denied page; same for templates and checks. (13) `MembersPage.test.tsx` — the role select offers "Viewer", "Operator", "Admin" (de: "Betrachter", …); `MemberPage.test.tsx` likewise with "Owner". (17) the invite form has the `novalidate` attribute and a malformed address shows the zod message. (18) `list-row.test.tsx` — `StaticListRow` renders no link and carries the row classes; `OrganizationsPage.test.tsx` — a non-member row is not a link and shows its title.
- [ ] **Step 2:** run the files, expect failures.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the files pass; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): settings and organization page leftovers from 7.74`.

### Task 15: 7.82 SPA test hygiene

**Files:** `web/src/features/settings/OrgGeneralPage.test.tsx`, `web/src/features/settings/SettingsLayout.test.tsx` (~44), `web/src/features/members/MemberPage.test.tsx` (~50), `web/src/features/users/UsersPage.test.tsx`, `web/src/features/instance/shared/SharedChecksPage.test.tsx` (~102) → new `web/src/features/instance/shared/SharedListFrame.test.tsx`, `web/src/features/instance/SmtpSettingsPage.test.tsx`, `BackupsPage.test.tsx`, `LanguageSettingsPage.test.tsx` (`ADMIN` → import from `web/src/test/renderRoutes.tsx`), `web/src/features/auth/ForgotPasswordPage.test.tsx`, `web/src/features/auth/ResetPasswordPage.test.tsx`, `web/src/features/instance/OrganizationsPage.test.tsx`, `web/src/features/account/AccountSecurityPage.test.tsx`.

- [ ] **Step 1:** fix the JSX indentation in `OrgGeneralPage.test.tsx`; replace each 50 ms sleep with waiting for a positive signal that the gated data has loaded (e.g. `await screen.findByRole('link', { name: 'API tokens' })` after the role-dependent query settled, or `waitFor` on the mock's call log), then assert the absences. `UsersPage.test.tsx` holds no sleep any more — **verify only** (its negative assertion at ~67 waits for a real change). Move the German-counts `describe` into `SharedListFrame.test.tsx` unchanged. Replace the three local `ADMIN` fixtures by the shared one.
- [ ] **Step 2: New tests:** `ForgotPasswordPage.test.tsx` — POST forgot answering 500 shows the generic failure text; `ResetPasswordPage.test.tsx` — a 429 shows `tooManyAttempts`; `OrganizationsPage.test.tsx` — `GET /admin/orgs` 403 shows the access-denied page; `AccountSecurityPage.test.tsx` — a step-up whose retry is refused again shows `account.stepUp.refusedAgain`.
- [ ] **Step 3:** `npx vitest run` on every touched file (each new test must pass against today's code; if one fails, it found a bug — fix it in this task and say so).
- [ ] **Step 4:** eslint; tsc.
- [ ] **Step 5: Commit** `test(web): wait for real signals, share the admin fixture and cover the 7.74 refusal branches`.

### Task 16: 7.82 E2E journeys

**Files:** `web/e2e/accounts.spec.ts`, maybe `web/e2e/env.ts` (a new person's address and TOTP state paths), `web/e2e/mailpit.ts`.

- [ ] **Step 1:** add `test('a person whose second factor was reset enrols a new one, then replaces the temporary password')`: the admin invites a new address over the API, the person accepts through the mailed link (`extractInviteLink`) and enrols TOTP (`enrolTotp`); the admin opens the account, runs "Reset second factor" and "Set temporary password" (with `proveSecondFactorIfAsked`); the person signs in with the temporary password, lands on `/account/enrol`, enrols a new factor, is sent to `/account/password`, changes the password and reaches the hosts page.
- [ ] **Step 2:** in the same journey, read the notice mails: `waitForMessageMatching(email, /second factor was reset/)` and `/password was reset/` — each names the sign-in link and carries no password.
- [ ] **Step 3:** add `test('someone who is not an instance administrator cannot create an organization')`: as the operator (fresh context, sign-in with its TOTP state), the org switcher shows no "New organization" entry, and `page.request.post('/api/v1/orgs', …)` answers 403.
- [ ] **Step 4:** `npx playwright test e2e/accounts.spec.ts` passes, then the whole E2E suite once (specs after `accounts.spec` rely on the operator's state — the new journeys must not change the operator's password or factor).
- [ ] **Step 5: Commit** `test(e2e): factor reset journey, notice mails and the refused organization create`.

### Task 17: File splits (Go)

**Files:** `internal/api/auth_handlers.go` → move the session gates (`requireSession` … `secondFactors`, ~lines 236-440) to new `internal/api/session_gates.go`. `internal/tenancy/service.go` → move the invitation code (`InviteTTL`, `inviteSendTimeout`, `inviteSecretBytes`, `Invite`, `AcceptInvite`, `checkAcceptor`, `inviteLookupErr`) to new `internal/tenancy/invites.go`, and membership changes (`ChangeRole`, `RemoveMember`, `DeactivateUser`) to new `internal/tenancy/members.go`. Tests: `internal/api/admin_user_handlers_test.go` → actions into new `admin_user_actions_test.go`; `internal/accounts/store_sql_test.go` → reset-token and action store tests into new `store_sql_actions_test.go`; `internal/accounts/reset_test.go` → redeem/link tests into new `reset_redeem_test.go`; `internal/api/must_change_password_test.go` → the route-table tests into new `must_change_password_routes_test.go`. Exact cut points follow each file's existing groups; every resulting file is under ~400 lines.

- [ ] **Step 1:** pure moves — no behaviour change, tests unchanged except moved; doc comments travel with their code.
- [ ] **Step 2:** `./internal/api/` (1800 s), `./internal/tenancy/`, `./internal/accounts/` pass with the same test count as before (`go test -list . ./internal/<pkg>/ | wc -l` before and after); `wc -l` of every touched file ≤ ~400; `golangci-lint run`.
- [ ] **Step 3: Commit** `refactor(api,tenancy,accounts): split the files past 400 lines`.

### Task 18: File splits (SPA)

**Files:** `web/src/router.tsx` → the settings subtree (`settingsRoute` … the instance routes, ~lines 265-350) into new `web/src/routes/settingsRoutes.tsx` exporting `settingsRouteTree(orgRoute)`; `web/src/features/hosts/HostDialog.tsx` (7.77 item 9) → `HostForm` and its helpers into new `web/src/features/hosts/HostForm.tsx`, the loader stays.

- [ ] **Step 1:** pure moves — no behaviour change, tests unchanged except moved.
- [ ] **Step 2:** `npx vitest run src/router.test.tsx src/features/hosts`; eslint; tsc; `wc -l` of the four files ≤ ~400.
- [ ] **Step 3: Commit** `refactor(web): split the router's settings subtree and the host form out`.

### Task 19: 7.64 leftovers

**Files:** none expected.

- [ ] **Step 1 (item 3):** `go test -race -count=20 -timeout 900s ./internal/backup/ 2>&1 | tee <scratch>/backup-race.log`. If it fails, keep the full output, find the cause with superpowers:systematic-debugging, fix it with a failing-first test, and commit `fix(backup): …`. If it passes, record the command, its duration and the pass in the report; no code change.
- [ ] **Step 2 (items 1, 2, 4, 5): already done, verify only.** (1) `deploy/compose/README.md` "Upgrading" step 4 already wraps the download in `if curl … && curl … && [ sha matches ]; then install && restart; else echo …; fi; rm -rf "$d"` (commits `0f3b667`, `ec1f2a5`); the controller updates noix-docs `admin/operations.md` with the same snippet. (2) `grep -rn "this release" internal/app deploy docs/upgrades.md` finds nothing and `restoreNextStep` says "Each one's log" (`3b3f249`). (4) `docs/upgrades.md` already says a scheduled backup interrupted by the upgrade's own stop is not mailed (`ec1f2a5`). (5) both comments were rewrapped in `c30bd1f`. Run the grep and `go test -race -count=1 -timeout 900s ./internal/app/` and record.
- [ ] **Step 3:** no commit unless Step 1 found a bug.

### Task 20: The runs host filter uses its GIN index under a generic plan (7.73)

**Files:** Create `internal/jobs/list_host_plan_test.go` (DB). Possibly modify `internal/db/queries/jobs.sql` (+ gen), `internal/jobs/store_sql.go` (`ListJobs` ~179).

**Interfaces:** test helper `generatedQuery(t, constName string) string` reads `internal/db/gen/jobs.sql.go` with `go/parser` and returns the raw SQL of the named const (`listJobs`), so the test explains exactly what the store runs. Seed (testdb, `testing.Short()` → `t.Skip`): one org, one zone, 200 host ids, 50 000 jobs with 1–3 random host ids each and spread `created_at`, inserted with one `INSERT … SELECT … generate_series` (required columns: `id`, `org_id`, `zone_id`, `kind`, `host_ids`, `run_group_id`), then `ANALYZE jobs`. In one transaction: `SET LOCAL plan_cache_mode = force_generic_plan`, `PREPARE list_jobs AS <sql>`, `EXPLAIN (FORMAT JSON) EXECUTE list_jobs(<org>, NULL…, <host>, NULL, NULL, 51)` in the order of `gen.ListJobsParams`; walk the plan JSON for `"Index Name": "jobs_host_ids_idx"`. Log `EXPLAIN (ANALYZE, FORMAT JSON)`'s execution time.

- [ ] **Step 1: Failing test** `TestListJobsHostFilterUsesTheGINIndexUnderAGenericPlan`. Expected to fail: the `sqlc.narg(host_id) IS NULL OR host_ids @> …` form leaves a generic plan nothing to index on.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run TestListJobsHostFilter ./internal/jobs/`. If it already passes, keep the test, skip Step 3, and record the plan in the report.
- [ ] **Step 3:** add `-- name: ListJobsForHost :many` — the same query with the host condition unconditional (`AND host_ids @> ARRAY[sqlc.arg(host_id)::uuid]`) and the other optional filters unchanged; `SQLStore.ListJobs` calls it when `f.HostID != nil`. Point the test at both: `listJobsForHost` must use `jobs_host_ids_idx`; existing `store_sql_test.go` host-filter tests stay green. Only if the planner still refuses the index, add an additive migration with a composite index and say why in its comment.
- [ ] **Step 4:** `./internal/jobs/` passes (and with `-short` the new test is skipped); sqlc diff clean.
- [ ] **Step 5: Commit** `test(jobs): prove the runs host filter uses its index under a generic plan` (+ `perf(jobs): give the host-filtered runs list its own query` when Step 3 ran).
