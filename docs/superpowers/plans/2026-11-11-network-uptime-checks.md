# Network and uptime checks (backlog 3.7) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The agent reports per-interface network rates and the host's uptime. Every organization gets both as seed checks, including existing organizations. The GUI can create and edit them.

**Architecture:** Two new agent plugins in `internal/agent/checks`, built the same way as cpu, memory and disk: a provider interface around gopsutil, fixture-driven tests, and tolerant collection. The manager learns the plugin names, their parameters and the seeds. A migration seeds existing organizations. The SPA adds both to its plugin lists and gives network an `interfaces` list, like disk's `mounts`.

**Tech Stack:**
- Go with gopsutil v4.26.10 (`net.IOCountersWithContext`, `net.InterfacesWithContext`, `host.UptimeWithContext`)
- Postgres migration via goose
- React SPA

**Spec:** Backlog 3.7 in `docs/BACKLOG.md` (six of eight system checks shipped; network and uptime have no plugin). The owner approved the scope on 2026-11-11 in chat:
- `network`: per interface, received and sent bytes per second, plus errors and dropped packets per second. An optional interface list; otherwise every real interface, without loopback and virtual container interfaces, analogous to disk's mount list.
- `uptime`: seconds since boot, so a rule can catch a restart.
- Both are created for every organization, like the four existing checks, at 1 or 5 minutes.

## Global Constraints

- Plugin names, exactly: `network` and `uptime`.
- Metric names, exactly:

  | Plugin | Metric | Item |
  |---|---|---|
  | network | `net.rx_bytes_per_s` | interface name |
  | network | `net.tx_bytes_per_s` | interface name |
  | network | `net.rx_errors_per_s` | interface name |
  | network | `net.tx_errors_per_s` | interface name |
  | network | `net.rx_drops_per_s` | interface name |
  | network | `net.tx_drops_per_s` | interface name |
  | uptime | `uptime.seconds` | the empty string (whole host) |

  Each metric name is at most 64 bytes. An item obeys `reportableItem`.
- Network rates are computed in the agent: counter delta divided by elapsed seconds between two collections of the same interface.
  - The first collection of an interface reports nothing for it.
  - A counter that went backwards (wrap, reset, reboot, re-created interface) reports nothing for that interface this tick and re-baselines it.
  - An interface that disappears is forgotten.
- Default interface set without the `interfaces` parameter:
  - every interface from `net.Interfaces` that is up and not loopback;
  - excluded by name prefix: `veth`, `docker`, `br-`, `virbr`, `cni`, `flannel`, `cali`, `vxlan`, `kube-`, `tun`, `tap`.

  An explicit `interfaces` list is taken verbatim and bypasses all filters, as disk's `mounts` does.
- Seeds:

  | Plugin | Interval | Binding |
  |---|---|---|
  | `network` | 60 s | org |
  | `uptime` | 5 min | org |

  Both have no params and origin `seed`.
- The migration is additive. For every existing organization it inserts the two seed rows with `ON CONFLICT DO NOTHING` on the definition key, so an operator's existing `network` or `uptime` key is left alone. Check the `check_definitions` unique constraint and mirror migration 00043's seeding SQL.
- Layering, testing and running per `CLAUDE.md`:
  - `go test -race -timeout 900s` on single packages; `internal/api` gets `-timeout 1800s`.
  - `golangci-lint run ./...` and `gofmt -s -l .` must be clean.
  - SPA checks from `web/`: `npx tsc --noEmit -p .`, `npx eslint src`, `npx vitest run`.
  - The API enum change needs `npm run api:gen` from `web/` (read `web/scripts/api-gen.sh` for how it reaches the spec).
- Conventional Commits. Footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push. Never tag.

## Review Focus

1. A counter reset or wrap never produces a huge or negative rate.
2. Two collections in the same second (elapsed time 0) never divide by zero.
3. An interface named in `interfaces` that does not exist reports an Err observation per metric for that item, as disk does for an unreadable mount. It is not silently empty.
4. The existing-org migration is idempotent and never fails on an org whose operator already used the key `network` or `uptime`.
5. An org whose seeds came from the migration and a new org seeded by `SeedForOrg` end up with identical rows, apart from ids and timestamps.

---

### Task 1: Agent plugins

**Files:**
- Create:
  - `internal/agent/checks/network.go` and `network_test.go`
  - `internal/agent/checks/uptime.go` and `uptime_test.go`
- Modify: `internal/agent/checks/plugin.go`. Add both to `Registry()`, and extend the package doc's plugin list.

**Interfaces:**
- `newNetworkPlugin() Plugin` and `newUptimePlugin() Plugin`.
- Network keeps its previous counters in a mutex-guarded map on a pointer receiver. The scheduler may call `Collect` concurrently with nothing else, but keep it safe.
- Inject a `now func() time.Time` for tests.

- [ ] **Step 1: Failing tests, using fixture providers only:**
  - the first collect reports nothing;
  - the second reports the six rates computed from the deltas;
  - a counter that went backwards reports nothing and re-baselines;
  - zero elapsed time reports nothing;
  - the default set skips loopback, down interfaces and every prefix in the list;
  - an explicit list bypasses the filters, and a listed interface missing from the counters gives one Err observation per metric;
  - an unreportable interface name is skipped;
  - a provider error gives an advisory error and keeps every other reading;
  - uptime reports `uptime.seconds` from the provider, and its provider error gives an Err observation.
- [ ] **Step 2:** Run `go test -race -timeout 900s ./internal/agent/checks/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the same command. Expected: PASS. Then lint.
- [ ] **Step 5:** Commit `feat(agent): network and uptime check plugins`.

### Task 2: Manager, API and migration

**Files:**
- `internal/checks/definitions.go`: plugin constants and the known-plugins set; `validateParams`, where network takes an optional non-empty-entry `interfaces` list and uptime takes no params.
- `internal/checks/items.go`: network's item list parameter is `interfaces`.
- `internal/checks/seed.go`: the two seeds.
- `internal/shared/service.go`: the plugin allow-list at about line 720.
- `internal/api/check_handlers.go`, `host_check_handlers.go` and `shared_handlers.go`: the `enum` tags.
- A new migration `internal/db/migrations/00084_seed_network_uptime_checks.sql`, with a goose Down that deletes only rows with origin `seed` and these two keys.
- Tests in each package touched, including a migration test in the style of the existing migration tests (find them under `internal/db`) and a seed test covering Review Focus 5.
- `web/src/api` regenerated schema.

- [ ] **Step 1:** Write failing tests:
  - validateParams for network and uptime, valid and invalid;
  - the items list for network;
  - `SeedDefinitions` contains both;
  - the migration seeds an existing org and skips one whose key is already taken;
  - the API accepts `plugin: "network"`.
- [ ] **Step 2:** Run the affected packages. Expected: FAIL.
- [ ] **Step 3:** Implement and regenerate the API types.
- [ ] **Step 4:** Run the packages (DB-backed ones one at a time; `internal/api` with `-timeout 1800s`), then lint. Expected: PASS.
- [ ] **Step 5:** Commit `feat(checks): seed and accept the network and uptime checks`.

### Task 3: SPA

**Files:**
- `web/src/features/checks/CheckDialog.tsx`: `PLUGINS`, and a network `interfaces` textarea like disk's mounts (optional; empty means every real interface).
- `web/src/features/instance/shared/SharedCheckFormPage.tsx`: the same.
- i18n en/de: plugin labels, the interfaces label and hint, and metric labels, if the SPA labels metrics anywhere (grep for `disk.used_pct` / `mem.used_pct` in `web/src`).
- Tests next to each.

- [ ] **Step 1:** Write failing tests:
  - the dialog offers Network and Uptime;
  - network sends `{interfaces: [...]}` or `{}`;
  - uptime sends no params;
  - the shared form behaves the same.
- [ ] **Step 2–4:** Implement, then run the SPA checks.
- [ ] **Step 5:** Commit `feat(web): network and uptime checks in the check forms`.

### Task 4: Backlog and upgrade note

- `docs/BACKLOG.md`: set row 3.7 to `done`, with a `**Done 2026-11-11.**` note naming the two plugins, their metrics and the seed migration.
- `docs/upgrades.md`: one newest-first bullet saying existing organizations get the two seed checks with the migration, and agents older than this release report "unknown plugin" for them until updated. Check how the agent answers an unknown plugin today and state that precisely.
- Commit `docs: close backlog 3.7`.

The end-user docs (noix-docs `reference/check-plugins.md`, `user/monitoring.md`) are updated by the controller in the docs repo.
