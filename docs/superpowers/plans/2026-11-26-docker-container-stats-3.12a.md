# Per-container resource usage for the docker check (backlog 3.12a) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog row 3.12a (`grep -n "^| 3.12a " docs/BACKLOG.md`).

**Spec (the backlog row):** "Per-container resource usage (cpu, memory, network, block I/O) for the docker check — needs the engine's stats endpoint, which is slow per container, and its own rate computation; split from 3.12". Builds on the 3.12 spec `docs/superpowers/specs/2026-11-12-docker-container-checks-design.md`.

**Architecture:** the agent's docker plugin (`internal/agent/checks/plugins/docker`) gains a `Stats` call on its engine client (`stats.go`) and a resource phase after the inspects (`resources.go`); the plugin becomes a `checks.StatefulPlugin` (like `network`) holding each sampled container's previous counters. The manager's descriptor (`internal/checkplugins/docker.go`) gains the `resources` parameter, the new metrics and a `MetricAsked` hook (like http's `http.body_match`); the rules catalogue, the API and the SPA form derive from it.

## Global Constraints
- Go layers per `CLAUDE.md`; `go test -race -timeout 900s ./internal/<pkg>/...`, never module-wide, never `make test`, never `internal/e2e`; `golangci-lint run` and `gofmt -s -l` clean on touched packages before each commit.
- No migration, no proto change. Both skew directions keep working (R9).
- Tests use a fake engine (`fakeDockerEngine`) or an httptest server on a Unix socket (`fakeEngine`); no real Docker, no sleeps as synchronisation.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no push, tag or merge.

## Rulings
- R1 **Opt-in** boolean parameter `resources` (default `false`) — an existing docker check does not start sampling, nor grow seven tuples per container against the 4 096 per-host tuple cap, on an agent upgrade; the stats endpoint is the engine's most expensive read — cost if wrong: users must tick a box to see usage; flipping the default later is a descriptor default plus the agent's parse.
- R2 **One-shot stats**: `GET /containers/{id}/stats?stream=false&one-shot=true` on the unversioned path (3.12's rule) — `one-shot` (API 1.41+) skips the engine's ~1 s wait for a second CPU sample; an older engine ignores it and is merely slow, which the deadline (R5) catches — cost if wrong: on engines before 20.10 most containers read "did not answer in time".
- R3 **Rates from the agent's own previous sample**, keyed by container **ID** (a recreated container with the same name re-baselines instead of reporting a bogus delta) and re-baselined when the inspect's `State.StartedAt` changes (a restart under the same ID resets the counters; review M3); elapsed time is the engine's `read` timestamp difference (not the agent clock, so request queueing does not skew it). First sight, a counter going backwards (per group: cpu, network, block) or a non-positive elapsed reports no rate for that group that round; memory needs no baseline — cost if wrong: one silent round after a restart or reset.
- R4 **Metrics** (item = container name, like 3.12): `docker.cpu_percent` (100 = one core on every platform, so rules mean the same across hosts; matches `docker stats` on Linux, while `docker stats` on Windows divides by the processor count), `docker.memory_bytes` (usage minus inactive file cache with the docker CLI's exact fallback order; Windows: private working set), `docker.memory_percent` (of the container's limit, the host memory when unlimited; Linux only), `docker.net_rx_bytes_per_s`, `docker.net_tx_bytes_per_s` (summed over the container's networks), `docker.block_read_bytes_per_s`, `docker.block_write_bytes_per_s`; plus `docker.containers_unsampled` on item `""` (R6). No limit-bytes or per-CPU metrics — cost if wrong: more metrics are additive later.
- R5 **Two phases under the existing 10 s collect deadline:** inspects first (unchanged), then stats with what remains, at most 8 in flight — a slow stats endpoint never costs the state metrics. A container whose stats fail or miss the deadline gets an error observation on each of its resource metrics (a missed deadline reads "engine did not answer in time", review M1); a 404 (gone) reports nothing — cost if wrong: on a very slow engine late containers show "collection failed" each round.
- R6 **Cap of 100 containers carrying usage** per round, fixed (not a parameter; controller rulings, fix rounds 1 and 2): running and paused ones first, by name, then remembered stopped ones (R7), by name; the rest get no resource metrics, are counted in `docker.containers_unsampled`, named by count in the advisory and lose their baseline and remembered metrics (a forgotten stopped container no longer counts) — deterministic (a deadline alone would fail different containers each round) and bounds usage tuples to 700 per round, also on a host that piles up exited job containers (review I1) — cost if wrong: hosts with more than 100 running containers must use `labeled` mode to choose which are sampled.
- R7 **A container that is not running or paused reads zero** without asking the engine, **only on the metrics its last successful running reading had** (controller ruling, fix round 1): CPU and block I/O always, memory when the reading has it, memory percent only with a limit (so never for a Windows container), traffic only with an own network mode and `networks` in the reading. That set is kept per container ID next to the baselines; a container this check instance never read running reports nothing while stopped. The same when the engine answers stats with a zero `read` (stopped between list and stats). A failed reading reports its error on that set, or on CPU and block I/O for a container not read yet — a periodic job container that exits must not turn its usage stale, and a value never reported while running must not appear while stopped and go stale when it runs again — cost if wrong: an agent restart while a container is stopped leaves its usage stale until it runs again.
- R8 **Network metrics only for a container with its own network**: the list's `HostConfig.NetworkMode` `host`, `none` or `container:<id>` reports no traffic, running or stopped (no phantom tuples that turn stale); otherwise traffic is reported when the stats carry `networks`. Block I/O counts a missing list as zero (cgroup v2 lists nothing for a container that did no I/O yet).
- R9 **Bounded state:** the baselines are replaced each round by the containers sampled this round; a sampled container whose stats failed keeps its old baseline; vanished, stopped, over-cap containers and `resources: false` drop theirs. The remembered metric sets (R7) hold only containers listed this round and not over the cap. **Skew:** an older agent ignores `resources` and reports no usage; an older manager never sends it. No manager-side gating — cost if wrong: none stored.
- R11 **Real-engine smoke test** (`smoke_test.go`, controller ruling, fix round 1): skipped unless `PULSIX_DOCKER_SMOKE=1` and a socket exists (`PULSIX_DOCKER_SOCKET`, `/var/run/docker.sock` or `~/.docker/run/docker.sock`); runs two rounds read-only (list, inspect, one-shot stats) and checks running containers' values are plausible. Never runs in CI or `make test`.
- R10 **Asked:** the resource metrics and `docker.containers_unsampled` are asked only while `resources` is `true` (`MetricAsked`), so switching it off reads their last values as not asked and closes their alerts as stale, like http's body match.

## Review Focus
- Container recreated under the same name → no rate on its first round (`TestDockerResourcesRecreatedContainerRebaselines`).
- Counter reset / restart → no negative or huge rate (`TestDockerResourcesCounterResetSkipsRate`).
- Stopped / exited containers keep reporting (zeros) on what they reported while running, no stats call (`TestDockerResourcesStoppedContainerReadsZero`, `TestDockerResourcesStoppedBetweenListAndStats`, `TestDockerResourcesNeverSeenRunningReportsNothing`, `TestDockerResourcesStoppedWindowsContainer`, `TestDockerResourcesStoppedWithoutNetworksInTheReading`, `TestDockerResourcesForgetsWhatAVanishedContainerReported`).
- Host-network container never reports traffic (`TestDockerResourcesHostNetworkReportsNoTraffic`).
- Slow stats never cost the state metrics; deadline errors per container (`TestDockerResourcesDeadlineKeepsStateMetrics`).
- Cap and concurrency (`TestDockerResourcesCapsSampledContainers`, `TestDockerResourcesBoundsConcurrency`).
- State growth (`TestDockerResourcesDropsVanishedBaselines`, `TestDockerResourcesKeepsBaselineOnFailedStats`).
- Parsing across cgroup v1/v2 and Windows (`TestClientStats*`), one-shot query (`TestClientStatsAsksOneShot`).
- Default off: no stats requests (`TestDockerResourcesOffByDefault`); `MetricAsked` (`TestDockerAsksForResourceMetrics`).

---

## Task 1: Agent client — the stats call
**Files:** `internal/agent/checks/plugins/docker/stats.go` (`containerStats`, `(*dockerClient).Stats`), `client.go` (`dockerEngine.Stats`, list's `NetworkMode`, 404 → gone for stats too), `stats_test.go`.
Tests: `TestClientStatsAsksOneShot`, `TestClientStatsParsesCgroupV2`, `TestClientStatsParsesCgroupV1`, `TestClientStatsParsesWindows`, `TestClientStatsStoppedContainer`, `TestClientStatsGone`, `TestClientStatsMalformed`, `TestClientListReadsNetworkMode`.
- [ ] Commit `feat(agent): docker engine client reads one-shot container stats`.

## Task 2: Agent plugin — resource phase and rates
**Files:** `resources.go` (`sampleResources`, `statsSample`, rate math), `docker.go` (`*dockerPlugin`, `NewInstance`, `resources` param, phase 2 after inspects, unsampled count), `resources_test.go`, `docker_test.go` (pointer plugin).
Tests: the Review Focus names above plus `TestDockerResourcesRates`, `TestDockerResourcesFirstRoundReportsMemoryOnly`, `TestDockerResourcesEngineDown`, `TestDockerNewInstanceStartsEmpty`, `TestDockerResourcesThroughTheClient` (Unix-socket httptest engine, two rounds).
- [ ] Commit `feat(agent): docker check samples per-container cpu, memory, network and block I/O`.

## Task 3: Manager descriptor, catalogue, SPA
**Files:** `internal/checkplugins/docker.go` (+ `docker_test.go`), `internal/rules/catalog_test.go` (golden), `web/src/test/checkPlugins.json` (`-update-spa-fixture`), `web/src/i18n/{en,de}/checks.ts`.
Tests: `TestValidateDockerParams` (resources cases), `TestDockerAsksForResourceMetrics`, catalogue golden, `paramTexts.test.ts`.
- [ ] Commit `feat(checks): opt-in container resource usage on the docker check`.

## Task 4: Docs
`docs/BACKLOG.md` row 3.12a → done; `docs/upgrades.md` (skew paragraph, behaviour note).
- [ ] Commit `docs: backlog 3.12a done`.
