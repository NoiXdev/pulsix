# Agent probe footprint (backlog 3.78) — implementation plan

**Goal:** close backlog row 3.78 (`grep -nE "^\| 3\.78 " docs/BACKLOG.md`): an agent probing for other hosts (3.13a) costs bounded memory while disconnected, does not link the snmp library it never runs, and cannot starve (or be starved by) its own checks at the manager's ingest budget.

**Architecture:** (1) `checks.Ring` gains an optional cost cap (`NewCappedRing`); the agent's probe ring is capped at an estimated 4 MiB, evicting oldest first and whole instants, and the loop logs what the cap dropped apart from what aged out. (2) A new package `internal/probe/vantage` wires the probes an agent may run (tcp, icmp, dns, http, tls) and their FailAlls; `internal/probe/all` is vantage plus snmp and stays the manager's registry; `internal/agent/probes` imports vantage only. (3) `agentapi`'s per-serial ingest bucket becomes one bucket per (serial, kind): check results and probe results each get `MaxTuplesPerHost` per `MinInterval` with a burst of twice that. No protocol, API, migration or stored-data change.

**Spec:** the backlog row. Context: `docs/superpowers/specs/2026-11-16-probe-vantages-design.md`, backlog 7.111 (ingest budget).

## Global Constraints
- Layers per `CLAUDE.md`. Tests: `go test -race -timeout 900s ./internal/{agent,agentapi,probe}/... ./cmd/agent/...`; never `go test ./...`, `make test` or `internal/e2e`. `golangci-lint run` and `gofmt -s -l` clean on touched packages.
- TDD; Conventional Commits with the `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` footer.

## Rulings
- **R1 The probe ring is capped by estimated bytes, 4 MiB.** Cost of a result = 128 bytes (the struct, 120, plus a value's 8-byte box) + the bytes of its item, metric, label and error text; the check key is shared with the slot's spec and not counted. — Why: bytes, not a count, because an error or label text is the one part that varies (a target's answer can be quoted). 4 MiB is about 30 000 results: 200 slots of 5 metrics at a 60 s interval fill a 15-minute window with 15 000, so at ordinary intervals the cap never bites and the time window rules; at 10 s intervals the ring keeps the newest few minutes instead of ~90 000 results (tens of MB). The slice may briefly hold twice its length in capacity while appending. — Cost if wrong: a long outage with short intervals loses its oldest minutes of probe results, which the target's other vantages and the manager's own probing usually cover.
- **R2 Over the cap, the oldest go first, and whole instants.** After the age eviction, entries leave from the front until the cost fits, then any further entries stamped with the same At as the last one evicted go too (a round's results are one contiguous Add and share one At), so no round is left half-buffered. — Cost if wrong: a few more results dropped than strictly needed.
- **R3 Only the probe ring is capped.** The agent's own check ring keeps its time-only bound: its size is set by the host's own checks (at most `MaxTuplesPerHost` tuples), not by up to 200 assignments, and the row is about the probe footprint. `Ring.Add` keeps its signature; `Ring.Put` returns `Evicted{Aged, OverCap}` for callers that say which.
- **R4 Drops are reported in the log, not a metric.** Pulsix has no metrics endpoint (backlog 3.27); the existing overflow line now names both counts (`aged_out`, `over_cap`) and the cap, at the same place (live sink while disconnected, failed replay put-back). — Cost if wrong: none; 3.27 can turn the counts into counters.
- **R5 `internal/probe/vantage` is the agent-side registry; `all` = vantage + snmp.** `vantage.Registry(gate)` and `vantage.FailAll` hold tcp, icmp, dns, http, tls; `all.Registry` adds `snmp.Probe` and `all.FailAll` tries snmp first, then vantage. `agent/probes.DefaultRegistry` is `vantage.Registry(nil)` (no `delete`). — Why: snmp is manager-only (its check always names a secret, and `checkplugins.VantageOf` validation refuses vantage groups on a check with a secret, still true after 3.134), so the agent never needs it; the import graph, not a runtime delete, is what keeps gosnmp out of the binary.
- **R6 An snmp slot on an agent fails with the descriptor's metrics.** It never arrives (the manager never assigns one and drops agent snmp results as unassigned); if it did, the agent now reports each snmp descriptor metric as an error on item "" instead of snmp's per-OID FailAll. — Cost if wrong: none observable; the manager discards it either way.
- **R7 A test pins the dependency list.** `cmd/agent` test runs `go list -deps` for linux, windows and darwin and fails on any `gosnmp` package or `internal/probe/snmp`; it skips when no `go` tool is on PATH.
- **R8 Two buckets per serial, same size each.** The registry keys buckets by `(serial, ingestKind)`; `ingestChecks` and `ingestProbes` each refill `MaxTuplesPerHost` per `MinInterval`, burst twice that. — Why: 4096 observations per 10 s is 20 per slot at 200 slots on the shortest interval and 120 at the common 60 s, which covers an http slot with assertions; a separate bucket means neither kind can delay the other's storage at the budget. The per-serial total is bounded at twice the old budget. — Cost if wrong: a flooding agent can store twice the observations per interval it could before (still bounded, still paced); the alternative (one bucket sized own+probes) would let probes starve checks again.
- **R9 Older agents are unaffected.** The kind is the message type (CheckResults vs ProbeResults) the manager already distinguishes; an agent without probes uses only the checks bucket, exactly as before. Note: both kinds still share one stream, so a probe batch waiting for its bucket delays the messages behind it on that stream (head-of-line); the budgets no longer drain each other, but one stream's order is kept.

## Review Focus
- A long disconnect with many short-interval slots keeps the ring under its cap, newest kept, no partial round (`TestCappedRingEvictsOldestWholeInstantsOverTheCap`, `TestProbeBufferStaysUnderItsCapDuringALongDisconnect` with 200 slots at 10 s for 15 minutes on a fake clock).
- The cap's drops are logged apart from age drops (`over_cap=` in the same test); the cost counts the variable text (`TestProbeResultCostCountsTheVariableText`, `TestCappedRingCountsCostNotEntries`).
- The age rule is unchanged for both rings (existing buffer tests stay green).
- The agent binary has no gosnmp (`TestAgentLeavesOutSNMP`); the manager registry still runs snmp (`TestRunKnowsExactlyTheServerPlugins` unchanged); the agent registry is exactly the server plugins but snmp (`TestVantageRegistryIsTheServerPluginsButSNMP`), whose secret stays required (`TestSNMPRequiresASecret`); an snmp slot on an agent fails with the descriptor's metrics (`TestSNMPSlotFailsWithTheDescriptorsMetrics`).
- Check results spent to the burst do not pace probe results, and vice versa; probe results over their own bucket are paced (`TestProbeResultsHaveTheirOwnIngestBudget`, `TestIngestReservationsPerKind`).

## Task 1: Cap the probe ring
**Files:** `internal/agent/checks/buffer.go` (+ `buffer_test.go`), `internal/agent/probes_wiring.go`, `internal/agent/probes_session_test.go`.
- [ ] Failing ring tests; `NewCappedRing`, `Put`, `Evicted`; probe ring capped with `probeResultCost`; log line. Commit `perf(agent): cap the probe result ring by size`.

## Task 2: Leave snmp out of the agent
**Files:** `internal/probe/vantage/vantage.go` (new, + test), `internal/probe/all/all.go`, `internal/agent/probes/{runner.go,execute.go}` (+ `execute_test.go`), `cmd/agent/deps_test.go` (new).
- [ ] Failing deps test; split; measure linux/amd64 size before/after. Commit `perf(agent): leave the snmp probe out of the agent binary`.

## Task 3: Separate probe ingest budget
**Files:** `internal/agentapi/{ingest_budget.go,stream.go,registry.go}` (+ `ingest_budget_test.go`, `probes_test.go`).
- [ ] Failing tests; per-kind buckets. Commit `perf(agentapi): give probe results their own ingest budget`.

## Task 4: Docs
- [ ] Backlog 3.78 → done; no `docs/upgrades.md` entry (no migration, protocol or API change). Commit `docs: backlog 3.78 done`.
