# Manager-side vantage costs (backlog 3.79) — implementation plan

**Goal:** close backlog row 3.79 (`grep -nE "^\| 3\.79 " docs/BACKLOG.md`): three manager-side costs of agent vantages (3.13a) go away without changing any output — the same assignments in the database, the same host checks response, the same `probes_supported` column.

**Architecture:** (1) the probe runner (`internal/probes`) remembers, per organization, a digest of the assignment set it last wrote and when; an equal set inside the rewrite period is not written. (2) The host page's vantage view (`probes.View.HostVantages`) reads the host's per-vantage rows and slot agents in two queries for all its vantage checks (`check_key = ANY($keys)`), not two per check. (3) The heartbeat's `probes_supported` flag rides the heartbeat `UPDATE` (`RecordAgentHeartbeat`); `SetProbesSupported` and its query go. No migration, no API or protocol change.

**Spec:** the backlog row. Context: `docs/superpowers/specs/2026-11-16-probe-vantages-design.md`, 3.87's lock-loss reset of per-slot state (`Runner.lost`).

## Global Constraints
- Layers per `CLAUDE.md`. Tests: `go test -race -timeout 900s ./internal/{probes,agents,agentapi,grpcsrv,agent}/...`, `./internal/api/...` with `-timeout 1800s -run`; never `go test ./...`, `make test` or `internal/e2e`. `golangci-lint run` and `gofmt -s -l` clean on touched packages.
- sqlc via `make sqlc`; no hand edits of generated code.
- TDD; Conventional Commits with the `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` footer.

## Rulings
- **R1 Skip on an equal digest, not on an equal slice.** The runner keeps per organization `writtenSet{digest [32]byte, at time.Time}`: SHA-256 over the canonical JSON of the set in its (already sorted) order, params as `encodeParams` renders them — the same canonical form the store diffs with. An encode failure is "no digest", which always writes. — Why: no reference to the plan's param maps outlives the refresh, 32 bytes per organization. Cost if wrong: a set that differs only in order is written once more (the store diffs it to nothing).
- **R2 Forced rewrite every 10 minutes (`assignmentRewrite`), empty sets included.** An equal set is written again once its last write is 10 minutes old. — Why: a change made outside the runner (a restore, a holder whose refresh overlapped) heals within 10 minutes; the same period as `configCheckRewrite`. Before, an empty set was never rewritten while the lock was held; now it is one small transaction per organization per 10 minutes, against one per organization per 30 s for every non-empty set before. Cost if wrong: an out-of-band change lives up to 10 minutes (before: 30 s for a non-empty set, until lock loss for an empty one).
- **R3 The cache is forgotten with the lock and on a failed write**, as `assignedEmpty` (which it replaces) was: `lost()` clears it, so a regained lock writes every organization once; a failed or interrupted write drops the organization's entry so the next refresh writes. A skipped write still adopts the plan as targets (the database holds that set). — Cost if wrong: none; this is the existing invalidation.
- **R3a (review) A lock taken again on a new session is a lost lock.** `schedule.AdvisoryLocker` counts the sessions it took the lock on (`Generation`); `probes.Locker` requires it, and `tickOnce` calls `lost()` when the generation differs from the one it held on the last tick — a session that died and was replaced between two ticks, while another manager may have held the lock and written its own assignments. This resets the assignment records and 3.87's per-slot state (`assertSince` through `regained`, `missing`, `next`, `pollers`, `expected`). The other users of the locker (retest, item expiry, retention purge, auth and invite reapers) lock once per pass and unlock at its end, so they keep no state across a session and are unchanged. — Cost if wrong: none; a needless reset costs one write per organization.
- **R3b (review) Records of unlisted organizations are dropped** at the end of a refresh that listed the organizations (`forgetWritten`), so the map does not grow with deleted organizations while the lock is held.
- **R4 The view batches by host, keeping the org scope.** `ViewStore` loses `VantageCurrent`/`SlotAgents` and gains `HostVantageCurrent(ctx, org, host, keys) (map[string][]VantageRow, error)` and `HostSlotAgents(ctx, org, host, keys) (map[string][]uuid.UUID, error)`, each one query with `org_id = $org AND {host_id|target_host_id} = $host AND check_key = ANY($keys)`, ordered as the per-check queries were (vantage, item, metric; agent host id). The runner's `VantageStore.VantageCurrent` stays (one slot per combine). `SlotAgents` and `ListProbeSlotAgents` go (no other caller). — Why: existing indexes cover both (`probe_current` PK leads with `host_id`, `probe_assignments_slot_idx (target_host_id, check_key)`), so no migration. Cost if wrong: none for output; the API test pins it.
- **R5 `probes_supported` is set in the heartbeat `UPDATE` itself**, unconditionally (`probes_supported = $x`): the statement rewrites the row anyway (`last_seen`), so the column costs nothing there and the separate statement — a round trip per heartbeat even when it matched no row — goes. `HeartbeatReport.ProbesSupported` carries it; `Touch` (no heartbeat) leaves it as it is. The flag is now written by serial (the heartbeat's key) instead of by host id: the same row for a live agent; a stream whose serial no longer matches (renewed or revoked) writes neither `last_seen` nor the flag, and an assignment needs both. A heartbeat whose write fails now fails the stream as before; the flag no longer has its own "logged, next heartbeat retries" path because it has no write of its own. `agents.Service.SetProbesSupported`, the store method and `SetAgentProbesSupported` go; test fixtures set the flag through a heartbeat. — Cost if wrong: none observable.
- **R6 Measuring.** Store calls are counted through a wrapper (`apitest.WithVantageStoreWrapper`), and the API test logs the request's wall time for a host with 20 vantage checks; before/after numbers are in the report.

## Review Focus
- An unchanged world writes no assignments on later refreshes, a change writes at once, the 10-minute mark writes again (`TestAssignmentsWrittenOnlyWhenChanged`, `TestAssignmentsRewrittenAfterThePeriod`).
- Lock loss and regain write again even when unchanged, also when TryLock re-acquired on a new session without ever answering false; a failed write is retried next refresh (`TestLostLockForgetsVantageState`, `TestReacquiredLockForgetsState`, `TestAdvisoryLockerGenerationCountsSessions`, `TestAssignmentWriteFailureRetriedNextRefresh`). Address-only and interval-only changes write (`TestAssignmentsWrittenOnAddressOrIntervalChange`); unlisted organizations lose their record (`TestAssignmentRecordForgetsDeletedOrgs`).
- Same host checks response for 20 vantage checks, with two store reads instead of forty (`TestHostChecksVantagesBatched` in internal/api); the existing `TestHostChecksReportVantages` stays green unchanged.
- Batched reads keep org and host scope: rows of another org or host never appear (`TestHostVantageReadsScoped` in internal/probes).
- The heartbeat writes the flag in its own statement and flips it both ways (`TestProbesSupportedAndOnlineCapableAgents` in internal/agents, which also shows a touch keeps it); agentapi passes it through (`TestHeartbeatCarriesProbesSupported`).

## Task 1: Batch the host page's vantage reads
**Files:** `internal/db/queries/probes.sql` (+ sqlc), `internal/probes/{view.go,store_sql.go}` (+ `view_internal_test.go`, `store_sql_test.go`), `internal/api/apitest/apitest.go`, `internal/api/host_check_vantages_batch_test.go` (new).
- [ ] API test with 20 vantage checks first (response pinned, store reads counted); see it fail on the count; batch; commit `perf(probes): read a host's vantage rows in two queries`.

## Task 2: Write assignments only when they change
**Files:** `internal/probes/{vantages.go,runner.go,targets.go}` (+ `vantages_internal_test.go`).
- [ ] Failing tests; digest cache; commit `perf(probes): skip an unchanged assignment write`.

## Task 3: Fold probes_supported into the heartbeat
**Files:** `internal/db/queries/agents.sql` (+ sqlc), `internal/agents/{service.go,store_sql.go,store_probes.go}` (+ tests), `internal/agentapi/{stream.go,probes.go}` (+ tests), `internal/grpcsrv/testfakes.go`, `internal/agent/integration_test.go`, `internal/probes/vantages_integration_test.go`.
- [ ] Failing tests; fold; commit `perf(agents): record probes_supported in the heartbeat update`.

## Task 4: Docs
- [ ] Backlog 3.79 → done; no `docs/upgrades.md` entry (no migration, protocol or API change). Commit `docs: backlog 3.79 done`.
