# One Package per Agent Check Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move each agent check plugin out of `internal/agent/checks` into its own package under `internal/agent/checks/plugins/<name>/`, without any behaviour change.

**Architecture:** `internal/agent/checks` keeps the plugin contract (`Plugin`, `Observation`, a newly exported `StatefulPlugin`), the shared observation and item-contract helpers (now exported), the scheduler and the buffer. Each plugin becomes package `cpu`, `memory`, `disk`, `network`, `service`, `uptime` or `docker` with a `New() checks.Plugin` constructor. `internal/agent/checks/plugins` holds `Registry()`, which imports all seven. Importing `plugins` from `checks` would be a cycle, so the agent loop calls `plugins.Registry()`.

**Tech Stack:** Go.

**Spec:** `docs/BACKLOG.md` row 3.52 (no separate design document; the owner agreed to the layout in chat on 2026-11-12).

## Global Constraints

- No behaviour change: same metrics, items, errors, labels, timeouts and per-OS behaviour. Every existing test keeps passing; tests move with the code they test and change only in package name, imports and the exported helper names.
- Plugin package names and directories exactly: `cpu`, `memory`, `disk`, `network`, `service`, `uptime`, `docker` under `internal/agent/checks/plugins/`. Each exports `func New() checks.Plugin` and nothing else unless a test in another package needs it.
- Registry: `func Registry() map[string]checks.Plugin` in `internal/agent/checks/plugins/registry.go`, same keys as today.
- Exported names in `checks`: `ValueObservation`, `LabelObservation`, `ErrObservation`, `ParamStrings`, `ReportableItem`, `ReportableLabel`, `MaxItemLen`, `MaxLabelLen`, `StatefulPlugin` (interface `{ Plugin; NewInstance() Plugin }`). Export `CutErrText` only if a plugin package calls it directly. Nothing else becomes exported.
- Per-OS files keep their build tags/suffixes and move into the owning plugin's directory (`service_*.go` → `service/`, `drive_*.go` → `disk/`, `docker_dial_*.go` → `docker/`). `platform.go` and `platform_test.go` are split by owner (mount discovery and inode support → `disk`, interface prefixes → `network`, anything cpu uses → `cpu`).
- File names inside a plugin package drop the plugin prefix where it only repeated the package (`docker/client.go`, `docker/dial_unix.go`, `service/states.go`, `service/linux.go` …); keep `_test.go`, `_windows.go`, `_darwin.go`, `_linux.go` suffixes intact.
- Repo rules: never `go test ./...`; run `go test -race -timeout 900s ./internal/agent/...` (CI does the same) and `golangci-lint run` plus `GOOS=windows golangci-lint run ./internal/agent/...` and `GOOS=darwin go vet ./internal/agent/...` / `GOOS=windows go test -c` compile checks for every moved package with OS-specific files. Coverage gate ≥ 80 % per package (scripts/coverage-gate.sh) must hold for every new package. Commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Use `git mv` so history follows the files.
- Never touch the Docker engine on this machine.

## Review Focus

1. A per-OS file that loses its build constraint or lands in the wrong package compiles on darwin but not on linux/windows — every moved package is compile-checked for linux, darwin and windows.
2. `network`'s per-key cloning: the scheduler must still detect the stateful plugin through the exported interface (a test in `checks` with a fake stateful plugin, plus the existing network test).
3. A new package below the 80 % coverage gate (small packages like `uptime` or `cpu` whose tests were partly elsewhere).
4. Comments elsewhere in the repo that cite moved file paths (`internal/rules/catalog.go` OBLIGATION list, `internal/checks/items.go`, `internal/checks/docker_params.go`, `internal/rules/catalog_test.go`) point at the new paths.
5. Test helpers shared between plugin tests (fakes, fixtures) are not duplicated verbatim across packages; a shared fake that more than one plugin package needs goes into an internal test-helper package or stays with its single user.

---

### Task 1: Export the shared plugin surface in `checks`

**Files:**
- Create: `internal/agent/checks/observe.go` (the observation helpers, `ParamStrings`, the item/label contract helpers and their constants, moved out of `plugin.go` and `disk.go`)
- Modify: `internal/agent/checks/plugin.go` (`StatefulPlugin` exported with `NewInstance`), `scheduler.go` (uses `StatefulPlugin`), `network.go` (`NewInstance`), every plugin file and test in the package (renamed call sites)

**Interfaces:**
- Produces the exported names listed in Global Constraints, used by Task 2's plugin packages.

- [ ] **Step 1:** Move and rename; update call sites. No logic change.
- [ ] **Step 2: Verify** `go test -race -timeout 900s ./internal/agent/...`, `golangci-lint run`, `GOOS=windows golangci-lint run ./internal/agent/...`, `GOOS=darwin go vet ./internal/agent/...`.
- [ ] **Step 3: Commit** `refactor(agent): export the check plugins' shared helpers`.

### Task 2: One package per plugin, registry in `plugins`

**Files:**
- Move (git mv) every plugin's files and tests into `internal/agent/checks/plugins/<name>/`; split `platform.go`/`platform_test.go` by owner.
- Create: `internal/agent/checks/plugins/registry.go`, `registry_test.go` (the registry test from `plugin_internal_test.go` moves here: same keys, every value non-nil, `network` is a `checks.StatefulPlugin`).
- Modify: `internal/agent/loop.go:193` and the tests in `internal/agent` that call `checks.Registry()` → `plugins.Registry()`; remove `Registry` from `checks/plugin.go`; update the `checks` package doc comment to describe what stays and point at `plugins/`.
- Modify: comments citing old paths (Review Focus 4).
- Modify: `docs/BACKLOG.md` row 3.52 → done, with a short note of the final layout and the exported surface (documentation folded into this task).

**Interfaces:**
- Consumes Task 1's exported names.
- Produces `plugins.Registry()` and `<name>.New()`.

- [ ] **Step 1:** Move the files, fix package clauses and imports, rename files per Global Constraints.
- [ ] **Step 2:** Add a test in `checks` (scheduler) that a fake `StatefulPlugin` is cloned per key via `NewInstance`, if no existing scheduler test already covers it through a fake.
- [ ] **Step 3: Verify** `go test -race -timeout 900s ./internal/agent/... ./cmd/agent/...`; `golangci-lint run`; for OS coverage: `GOOS=windows go test -c -o /dev/null` and `GOOS=linux go test -c -o /dev/null` for each package under `./internal/agent/checks/...` (`go vet` with GOOS=linux, darwin and windows over `./internal/agent/...` is enough); `scripts/coverage-gate.sh` (or `make test`) for the gate.
- [ ] **Step 4: Commit** `refactor(agent): move each check plugin into its own package` (the backlog edit may be its own `docs(backlog): …` commit).
