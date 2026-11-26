# Run request approval: the loose ends 2.126 left (backlog 2.127) — implementation plan

**Goal:** close backlog row 2.127 (`grep -nE "^\| 2\.127 " docs/BACKLOG.md`). Items (2) and (4) are done. This plan closes the race of item (5), and decides items (3) and (1).

**Architecture:** the claim of a run job whose guard says "approval withdrawn" fails the job only after re-reading the run's request under a row lock, inside the transaction that writes the failure (`internal/jobs` queue + a new `ClaimGuard` method implemented by `internal/runrequests`). No migration, no proto or API change.

**Spec:** the backlog row; context in `docs/superpowers/plans/2026-11-24-approval-loose-ends-2.126.md` (Ruling 1) and `internal/jobs/claim_withdrawn.go`.

## The race (item 5)

1. An approval enqueued zone A's job, failed before zone B's and put the request back to pending.
2. A worker claims A's job (committed: `running`, attempts 1). The guard (`CheckRunClaim`) reads the request: pending, so `ErrApprovalWithdrawn`.
3. In the window, a person approves again: `Decide` commits `approved`, the replay (`reserveRunGroup`) lists the group and sees A's job `running`, so A's zone counts as covered; only B is enqueued.
4. The queue's failure of A's job commits (`WithdrawnAtClaimError`). A's zone never runs; the group's outcome leaves the withdrawn job out and reports B's result alone.

## Global Constraints
- Go layers per `CLAUDE.md`; `go test -race -timeout 900s ./internal/{jobs,runrequests,grpcsrv}/...`, `./internal/api/...` with `-timeout 1800s -run`; never `go test ./...`, never `make test`, never `internal/e2e`. `golangci-lint run` and `gofmt -s -l` clean on touched packages.
- sqlc via `PATH=$PATH:~/go/bin make sqlc`; no hand edits of generated code. No migration (a query only).
- TDD; Conventional Commits with the `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` footer.

## Rulings
- **R1 Close the window with a row lock on the request, taken by the failing claim.** On `ErrApprovalWithdrawn` the queue fails the job through `terminate` with a new precondition that runs first inside the same write transaction: `ClaimGuard.ConfirmWithdrawn(ctx, org, group)`, which reads the request `SELECT status … WHERE org_id AND run_group_id … FOR SHARE` and reports whether it is still not approved. If it is approved by now, nothing is written and the guard is asked again (the job then runs). — Why it closes the window under READ COMMITTED: `Decide` is an `UPDATE` of the same row and takes `FOR NO KEY UPDATE`, which conflicts with `FOR SHARE`. (a) The claim locks first: `Decide` waits until the job's failure has committed; the replay's `ListJobs` is a later statement after `Decide` returned, so its snapshot sees the job failed as withdrawn and refills its zone. (b) `Decide` writes first: the claim's locking read waits for its commit and then reads the newest row version (a locking read in READ COMMITTED follows the update chain and re-checks its `WHERE` against it; `run_group_id` stays the request id), so it sees `approved`, writes nothing, the guard passes and the job runs; the replay saw it `running` and does not refill. (c) `Decide` committed before the locking read: a plain case of (b). In no order does the replay read a job that is about to be failed as withdrawn. — Cost if wrong: the window stays open (as today).
- **R2 The lock is the request row, not an advisory lock.** The row is what `Decide` already writes, so the approval side needs no change and no new lock key; `FOR SHARE` (not `FOR KEY SHARE`, which does not conflict with a non-key update). — Cost if wrong: none beyond R1.
- **R3 `ConfirmWithdrawn` is a required method of `jobs.ClaimGuard`**, not an optional interface: a guard that can say "withdrawn" must be able to confirm it, else the race is back silently. Test guards in `internal/jobs` gain the method. A run group without a request confirms as withdrawn only if the guard said so; `ConfirmWithdrawn` answers true when no request is found (nothing can approve it). — Cost if wrong: a compile error in an out-of-tree guard (there is none).
- **R4 Bounded re-asks.** The guard is asked at most 3 times; a request that flips between approved and withdrawn that often while one job is claimed is failed with an ordinary refusal (`refused at claim: the run's approval changed while it was claimed`), which counts as an errored run and covers its zone. — Why: a loop must end; failing visibly is the safe side. Cost if wrong: one run fails in a case that needs two failed enqueues inside one claim.
- **R5 Item (3) not done.** `render.Carries` and `render.Render` share `newRenderer`/`selection`; reusing one renderer across both would carry the group index built before `reveal` replaces the vars (`r.groups` holds copies whose `Vars` stay sealed; today only their `Schema` is read there), and it would replace the test seam `Renderer.carries func(render.Input)` the 2.126 equivalence tests swap. Building two maps over the organization's groups and hosts costs microseconds next to the reads and decryptions of a snapshot. — Cost if wrong: none measurable.
- **R6 Item (1) not done; recorded in the backlog row.** Host names are reachable (the request-limit narrowing already does it in `runrequests.narrowTargets`), but the picture's target list is also what the vars digest binds and what the enrollment list and the only-host check are taken from. Narrowing it per host either drops, from what the approval binds, the vars of hosts the claim still renders and ships to the worker (a play can read them through `hostvars`) — a security trade-off for the owner — or needs the picture to carry two host lists (rendered vs. played) through digest, enrollment and only-host check. It also changes the fingerprint of every limited template's request once more after rc.6. — Cost if wrong: the picture keeps showing a host the limit excludes, with the limit shown next to it.

## Review Focus
- The interleaving of the backlog row, deterministically: the guard reads pending, the re-approval runs in full, then the failure is written → the job runs instead (`TestRunRequestReapprovalWhileAClaimIsRefusedRunsTheZone`).
- The other order: the failure holds the lock while the re-approval starts → the approval waits, then refills the zone (`TestRunRequestReapprovalWaitsForAWithdrawnClaimAndRefillsTheZone`, waits on `pg_stat_activity` for the blocked `DecideRunRequest`).
- The bounded loop and the plain paths at the queue (`TestClaimWithdrawnIsConfirmedUnderTheRequestLock` table in `internal/jobs`).
- `ConfirmWithdrawn` against the real store for pending, approved and unknown groups (`TestConfirmWithdrawn*` in `internal/runrequests`).

## Task 1: Reproduce, then confirm the withdrawal under the request's lock (item 5)
**Files:** `internal/api/run_request_reapproval_race_test.go` (new), `internal/jobs/{claim_withdrawn.go,queue_sql.go,terminal.go}` (+ `claim_withdrawn_confirm_store_test.go`, test guards in `claim_guard_store_test.go`, `claim_withdrawn_store_test.go`, `list_page_store_test.go`), `internal/db/queries/run_requests.sql` (+ sqlc), `internal/runrequests/{approve.go,service.go,store_sql.go}` (+ `service_test.go` memStore, `store_sql_test.go`).
- Interface: `ClaimGuard.ConfirmWithdrawn(ctx context.Context, orgID, runGroupID uuid.UUID) (bool, error)`; `runrequests.Store.LockStatusByRunGroup(ctx, orgID, group) (Status, error)`; `terminalTransition.precondition func(ctx context.Context) (bool, error)`.
- [ ] Failing API test first (the wrapper guard runs the re-approval after the real guard's withdrawn answer), watch it fail; implement; second API test and the jobs/runrequests tests; lint; commit `fix(jobs): confirm a withdrawn approval under the request's lock before failing the claim`.

## Task 2: Docs
- [ ] Backlog 2.127 → done (items 5 done, 3 and 1 recorded as not done with the reasons above); `docs/upgrades.md`: no upgrade effect (no migration, protocol or API change) — no entry. Commit `docs: backlog 2.127 done`.

## Addendum: backlog 2.141 (the job's frozen hosts against the approved picture)

**Goal:** a run request's job never renders a host the approval did not include. A job freezes its host list at enqueue, after the approval read the picture; a host that joins the template's hosts in between and leaves before the claim is in the job but in neither picture, so the fingerprint check passes. Ruling from the coordinator: fail closed at the claim, visibly; fewer hosts than approved is fine; ordinary runs unaffected.

### Rulings
- **R7 The approved target set is the live picture, read after the fingerprint matched.** `CheckRunClaim` already recomputes the picture and compares its fingerprint with the approved one; `Fingerprint` covers `Targets` (sorted by id), so a match means the live targets are the approved targets. Nothing new is stored, no migration. — Cost if wrong: none; a fingerprint mismatch already refuses the claim before this check.
- **R8 The job's hosts reach the guard as `ApprovedRun.HostIDs`**, set by the claim only (`guardClaim`); an approval stores none and every other user ignores it. — Why: no signature change of the `ClaimGuard` port. Cost if wrong: a confusing field, documented on the type.
- **R9 Any job host outside the targets refuses the claim with `ErrHostsChangedAfterApproval`** ("the run's hosts changed after it was approved; ask for the run again (host <id> was not part of the approved run)"), an ordinary refusal: the run reports failed and the job covers its zone (no refill on re-approval, which needs a new request anyway). Fewer hosts (a host left, the enqueue excluded one) pass. — Cost if wrong: a run fails that would have reached only approved hosts; impossible, since the check is a subset test.
- **R10 Every run request path keeps job hosts within the targets today:** the template's own limit (targets are the kept zones' full host lists, `LimitedRunTargets`, the same gate the enqueue uses), a request's overrides (the enqueue's `OnlyHosts` are the picture's narrowed targets, `runOverrides`), one host (`OnlyHost`), a retry (targets are the failed hosts, the retry's scope). So the check refuses only the race. This also corrects a concern raised in the 2.127 report: with a limit override the job is narrowed to the targets, so no unapproved host ships.

### Review Focus
- The race itself, deterministically: a request limited to `web-1*`; an `AFTER UPDATE` trigger on `run_requests` renames host B (`web-2`, managed throughout) to match inside the approval's decision (after the picture read, before the enqueue narrows the run to the matching hosts), and the test renames it back before the claim and checks the fingerprint is unchanged. Without the guard zone B's claim renders `web-2` with no limit (a real leak); with it the claim is refused and zone A's job is claimed (`TestRunRequestClaimRefusesAHostTheApprovalDidNotInclude`, internal/api, two sub-tests).
- The subset test (`TestCheckRunClaimComparesTheJobsHosts`, internal/runrequests); the queue hands the job's hosts to the guard (`TestQueueClaimGuardSeesTheJobsPin`, internal/jobs).

### Task
**Files:** `internal/jobs/{run_request.go,claim_withdrawn.go}`, `internal/runrequests/approve.go` (+ `service_hosts_test.go`), `internal/jobs/claim_guard_store_test.go`, `internal/api/run_request_hosts_changed_test.go`, `docs/upgrades.md`, `docs/BACKLOG.md`. Commit `fix(runrequests): refuse a claim whose hosts the approval did not include`.
