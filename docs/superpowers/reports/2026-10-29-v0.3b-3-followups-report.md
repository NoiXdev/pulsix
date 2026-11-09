# v0.3b-3 follow-ups — report

## Important: worktree/branch mismatch

The task said to work from
`/Users/noidee/kepler/worktrees/server_manager-task-77cc3d89-05a0c4e9` on
branch `feat/v0.3b-3-followups`, "already checked out". In fact that
worktree was checked out on `feat/v0.3b-3-discovery`, and
`feat/v0.3b-3-followups` was checked out in a *different* worktree,
`/Users/noidee/_dev/server_manager` (which is also where my Bash tool's
cwd actually started, per the environment block).

I did not notice this until after committing 3.39 in the wrong place. I
fixed it immediately:

- Saved the diff as a patch, soft-reset the wrong worktree back to the
  shared base commit (`8bf4025`), reversed the working-tree changes with
  `git apply -R`, and confirmed with `git diff 8bf4025 --stat` that the
  wrong worktree (`feat/v0.3b-3-discovery`) is byte-for-byte back to its
  original state (verified again at the end — see "Final state" below).
- Re-applied the same patches in the correct worktree
  (`/Users/noidee/_dev/server_manager`, branch `feat/v0.3b-3-followups`)
  and did all subsequent work, and all five commits, there.

All five commits described below are on `feat/v0.3b-3-followups` at
`/Users/noidee/_dev/server_manager`. **This report file was written to the
path you specified, in the other worktree, since you asked for it there
and said you'd delete it — but the actual commits are in the other
location.** `/Users/noidee/kepler/worktrees/server_manager-task-77cc3d89-05a0c4e9`
itself was left exactly as found (clean, at `8bf4025`, branch
`feat/v0.3b-3-discovery`).

## Commits (in `/Users/noidee/_dev/server_manager`, branch `feat/v0.3b-3-followups`)

Post-review update: the reviewer flagged that bundling all four backlog
row updates into one trailing commit (`ec73c65`, superseded below) broke
the "one commit per item, revert any one alone" property for the one file
whose job is telling the truth about what is in the tree — reverting, say,
`3140e9b` (3.40's code) would have left `BACKLOG.md` still claiming 3.40
done. Fixed by splitting that commit into four, one per row, each naming
exactly the item it closes (chose this over folding the docs change into
each code commit via rebase, since it needed no rewrite of the four
already-reviewed code commits — only the trailing docs commit was
replaced). History was rewritten (branch not pushed); content is byte-
identical (`git diff ec73c65 HEAD -- docs/BACKLOG.md` is empty), confirmed
by re-running `go build ./...` and the race suite for both packages
afterwards.

Final history, oldest to newest:

1. `6849108` fix(rules): make WindowRequests skip items an operator ignored (3.39 code)
2. `3140e9b` perf(items): push CheckKey and the pattern into org-scope Preview's query (3.40 code)
3. `6d598aa` test(rules): pin that Decisions is read once per host, not per item (3.41 code)
4. `766e72e` fix(web): give RuleItemPicker's datalist a unique id per instance (3.42 code, datalist half)
5. `e8fa78f` docs: close backlog 3.39 (WindowRequests is now ignore-aware)
6. `a7409ec` docs: close backlog 3.40 (Preview pushes CheckKey/pattern into the query)
7. `54d7378` docs: close backlog 3.41 (Decisions call count is now pinned)
8. `20a02bb` docs: record the datalist half of backlog 3.42 as shipped

Each docs commit touches exactly one `BACKLOG.md` row (commit 5 also
carries the file's "Last updated" date bump, since that is whole-file
metadata rather than belonging to any one item).

## 3.39 — WindowRequests is not ignore-aware

File: `internal/rules/evaluate.go`

`WindowRequests` built its `reported` set from `measured(ts)` alone. Added
the same `!s.Ignored[itemKeyOf(ts.Tuple)]` check `Evaluate`'s own per-tuple
skip applies, so an ignored item's tuple is treated as "not reported" for
window-collection purposes and no `WindowRequest` is ever built for it.

Test: `TestWindowRequestsIgnored` (`internal/rules/evaluate_test.go`) — two
items both named by a rule with a window on both trigger and clear; one
item's key is in `Ignored`. Asserts the window requests list contains only
the non-ignored item's two requests. This test fails without the fix (the
ignored item's requests would appear in `got`).

Nothing undecided here — the brief described the fix precisely and it was a
clean, local change.

## 3.40 — org-scope Preview fetches every item and filters in Go

Files: `internal/items/service.go`, `internal/items/service_test.go`

`Preview`'s org-scope branch now calls
`s.store.ItemsForOrg(ctx, orgID, Filter{CheckKey: d.CheckKey, ItemGlob: d.Pattern})`
instead of `Filter{}`, and builds `out.Matches`/`out.Total`/`out.Capped`
directly from the (already filtered) `views` slice — no more per-row
`CheckKey`/`Match` check for this branch. The host-scope branch is
unchanged: `ItemsForHost` has no `Filter` parameter, and since it already
bounds the fetch to one host there was no org-wide materialisation to
avoid there, so it keeps filtering in Go exactly as before (the brief's own
title is "**org-scope** Preview...", and I read that as deliberately
excluding the host-scope branch from this fix).

`d.Pattern` is valid to pass straight through as `Filter.ItemGlob`
regardless of `MatchKind`: `validateMatch` already refuses a `*` inside an
exact-match pattern, so an exact pattern is, by construction, already a
glob matching only its own literal value — no new validation needed, and
`in.Pattern` was already run through `ValidPattern` inside `validateMatch`
before this code path is reached.

**How I kept Total honest:** the SQL-pushed-down shape doesn't give up
exactness — `ItemsForOrg`'s `LIKE` translation (`globToLike`) is already
documented as an *exact*, not approximate, translation of `items.Match`'s
semantics (see `internal/items/store_sql.go`'s own comment on
`globToLike`), so the set of rows returned by the filtered query is
identical to the set the old Go loop would have kept. `Total` becomes
`len(views)` — every matching row, no cap — and the existing tests that
pin `Total`/`Capped`/`len(Matches)`
(`TestPreviewCapsMatchesButReportsTrueTotal`,
`TestPreviewAtExactlyLimitIsNotCapped`, `TestPreviewExactPatternMatchesOne`,
`TestPreviewGlobMatchesSeveral`, `TestPreviewGlobMatchesNone`,
`TestPreviewIgnoresVerdictAndNoteValidation`) all still pass unmodified,
because `fakeStore.ItemsForOrg` in the test file already implements the
`Filter` contract correctly (it was written that way for the unrelated
`TestItemsForOrgRejectsAnOverLongGlob`/`TestItemsForHostAndItemsForOrgPassThrough`
tests) — so the fake computes the identical result set whether the
production code passes `Filter{}` and narrows in Go, or passes the real
filter and trusts it. That means none of the pinning tests alone can prove
the push-down actually happened; only a test that inspects *what the store
was asked for* can.

New tests to close that gap:
- `TestPreviewOrgScopePushesFilterIntoQuery` — adds an `orgFilters
  []items.Filter` recorder to `fakeStore.ItemsForOrg`, calls `Preview` with
  `CheckKey: "disk", Pattern: "/var*"`, and asserts the recorded filter is
  `exactly` `{CheckKey: "disk", ItemGlob: "/var*"}` — not the zero value.
  This is the test that actually fails without the fix (reverting the
  production change makes it record `Filter{}`).
- `TestPreviewHostScopeDoesNotCallItemsForOrg` — a host-scoped preview must
  never reach `ItemsForOrg` at all; asserts `orgFilters` stays empty.

Nothing left open here; the brief's own flagged risk (pushing the filter
down changing how `Total` is computed) turned out not to bite, for the
reason above.

## 3.41 — nothing pins "Decisions is read once per host"

File: `internal/rules/due_sql_test.go`

Added `decisionsCalls int` to `fakeItemStore`, incremented in its
`Decisions` method. Extended `TestLoadSeenItemsDropsEmptyAndDedupesDuplicates`
with a second item (`/opt`, alongside the existing `/var`) specifically
because with only one item in play, "once per host" and "once per item"
are the same number — the counter would pass even if someone moved the
call into a per-item loop. With two items, `fake.decisionsCalls != 1` now
actually distinguishes the two shapes. I also had to fix the expected
`want` order for `fake.discovered`: `ListCheckCurrent` orders rows by
`(check_key, item, metric)`, so `/opt` sorts ahead of `/var` — the original
single-item test didn't have to care about order.

This test needs the Postgres test container (`testdb.Open`), so it only
ran as part of the full `internal/rules` race suite, not in isolation.

## 3.42 — datalist half only

File: `web/src/features/checks/RuleDialog.tsx`

`RuleItemPicker` now calls `useId()` and uses the result for both the
`Input`'s `list` attribute and the `<datalist>`'s `id`, replacing the fixed
`"rule-item-options"` string. No new dependency (`useId` is from `react`,
already imported in this file). Grepped the rest of `web/src` for
`"rule-item-options"` — no other references, so nothing else needed
updating.

Left `HostChecksCard.tsx`/`RuleDialog.tsx`'s file-size split alone, as
instructed.

BACKLOG.md's row 3.42 status is `in progress` (not `done`), with notes
recording the datalist half as shipped and the split half as still open —
following the house style for a half-true row (see 1.12, 1.19).

## Decision not covered by the brief: the three "stale comments" were already fixed

I read all three locations named in the brief before touching anything:

- `web/src/api/hooks/useItems.ts:91-93`
- `web/src/features/hosts/EffectiveRulesPanel.tsx:42-44`
- `web/src/features/checks/ItemDecisionDialog.tsx:252`

All three are **already correct** in the repo as it stands. The commit
`8bf4025` — `docs(web): stop describing the items read HostChecksCard no
longer makes` — which is the common base commit both
`feat/v0.3b-3-followups` and `feat/v0.3b-3-discovery` branched from,
already made exactly these three fixes (its own commit message describes
the identical defect the brief describes). `useItems.ts` already says
HostChecksCard does *not* consume that hook and explains why that lookup
must not come back; `EffectiveRulesPanel.tsx` already says the same;
`ItemDecisionDialog.tsx` already has a separate `PATTERN_MAX_UTF16`
constant distinct from `PATTERN_MAX_BYTES`, used for `maxLength`.

I made a redundant rewording edit to `useItems.ts` before realising this,
then reverted it (confirmed via `git diff` showing no change against HEAD
for all three files). No commit touches any of these three files — there
was nothing left to do.

This is worth flagging because the brief was written as if these were
still open; by the time I started, the base branch already carried the
fix. Worth double-checking the brief was generated from a slightly stale
view of the tree before trusting the "three stale comments" framing for
any future batch.

## Environment note: no Node/npm/pnpm on PATH by default

The Bash tool's default `$PATH` has no `node`/`npm`/`pnpm`. I found
`nvm` installed at `~/.nvm` with Node v22.23.2 and v24.14.0 available, and
used `v24.14.0` for everything below by sourcing `nvm.sh` and running `nvm
use v24.14.0` at the start of each frontend command. Flagging this in case
it is not expected to be missing from `$PATH` by default in this harness.

## Commands run and results

Go side (from `/Users/noidee/_dev/server_manager`):
- `go build ./...` — clean, no output.
- `gofmt -s -l internal/rules internal/items` — no output (clean).
- `golangci-lint run ./internal/rules/... ./internal/items/...` — `0 issues.`
- `go test -race -count=1 ./internal/items/... ./internal/rules/...` — both
  packages `ok` (run more than once across the session as changes landed;
  final run before writing this report was clean).
- Did **not** run `./internal/api/...` or `./internal/e2e/...`, per
  instructions.

Web side (from `/Users/noidee/_dev/server_manager/web`, Node v24.14.0 via nvm):
- `npm run test -- --run` → `Test Files 90 passed (90)`, `Tests 657 passed
  (657)`. (The repeated "Not implemented: Window's scrollTo()" lines are
  jsdom warnings, not failures — pre-existing, unrelated to this change.)
- `npm run build` → `tsc -b && vite build` succeeded, bundle emitted. (The
  "chunks larger than 500 kB" warning is vite's stock advice, pre-existing,
  unrelated to this change.)
- `npm run lint` → exit 0, no output (clean).
- `npm run api:gen` → regenerated `src/api/schema.d.ts` from the manager's
  live OpenAPI doc; `git status --short` showed no diff afterwards — no
  drift, as expected since none of the Go changes touch any HTTP-facing
  shape.

## Anything I could not close

Nothing from the assigned batch. The one open item is the backlog's own
3.42 split half, which the brief explicitly says to leave open.

The worktree/branch mix-up above cost time but left no residue: the wrong
worktree (`feat/v0.3b-3-discovery`) is confirmed clean and unchanged from
its starting commit `8bf4025`, and all real work landed only on
`feat/v0.3b-3-followups` in `/Users/noidee/_dev/server_manager`.
