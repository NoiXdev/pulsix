# Hardware Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Three agent check plugins — `temperature`, `smart`, `raid` — with manager validation, rules catalogue entries, SPA support and docs.

**Architecture:** Each plugin is its own package under `internal/agent/checks/plugins/<name>/` exporting only `New()`, registered in `plugins.Registry()`, with a provider seam so tests use fixtures. The manager accepts the plugins and their list parameters; the rules catalogue and the SPA learn the metrics. Follow the "Adding a plugin" checklist in `internal/agent/checks/plugins/registry.go`.

**Tech Stack:** Go (gopsutil v4 `sensors` for temperatures, `os/exec` for smartctl, `/proc/mdstat` parsing), React/TypeScript SPA.

**Spec:** `docs/superpowers/specs/2026-11-12-hardware-checks-design.md`

## Global Constraints

- Plugin names and check keys: `temperature`, `smart`, `raid`. Parameters: `sensors`, `devices`, `arrays` respectively — each an optional list of strings; non-empty entries, at most 64 entries, each at most 255 bytes; unknown parameters refused. `devices` entries must be absolute device paths (`/dev/…`, or the Windows forms smartctl accepts — the builder pins them in Task 1 and Task 3 together).
- Metrics exactly as the spec lists them: `temp.celsius`, `temp.high_celsius`, `temp.critical_celsius`; `smart.passed`, `smart.temperature_celsius`, `smart.power_on_hours`, `smart.reallocated_sectors`, `smart.pending_sectors`, `smart.offline_uncorrectable`, `smart.nvme_percentage_used`, `smart.nvme_available_spare_pct`, `smart.nvme_media_errors`; `raid.state` (label: `clean`, `degraded`, `resyncing`, `recovering`, `checking`, `reshaping`, `inactive`), `raid.devices_total`, `raid.devices_active`, `raid.devices_failed`, `raid.sync_pct`, `raid.arrays` (item `""`). Each "only when reported" metric is absent otherwise.
- smart: exec smartctl directly (never a shell), fixed argument list, JSON output, `-n standby` (a standby disk reports nothing that round); ≤ 4 devices at once, 20 s per device, whole collection ≤ 60 s, ≤ 1 MiB output per device; exit-status bits that report SMART findings are data; missing smartctl → one collection error on item `""` telling to install smartmontools.
- raid: Linux only (`/proc/mdstat`); elsewhere "not supported on this platform".
- temperature: none readable → collection error "no temperature sensors found" on item `""`, metric `temp.celsius`.
- Every item and label passes `checks.ReportableItem` / `checks.ReportableLabel` (skip and name the item in the advisory error otherwise, as disk/docker do).
- No seeding. No test touches real hardware or runs the real smartctl.
- Repo rules: TDD; never `go test ./...` (single packages, `-race -timeout 900s`; internal/api `-timeout 1800s -run …`); `golangci-lint run` and `GOOS=windows golangci-lint run ./internal/agent/...` clean; `GOOS=linux|darwin|windows go vet ./internal/agent/...`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; public docs name no tech stack; never touch the unrelated Docker containers on this machine or `~/_dev/noix-docs`.

## Review Focus

1. smartctl output that is valid JSON but lacks the expected blocks (USB bridge, virtual disk, `--scan` listing a device that then fails to open) — no panic, the right metrics absent, a per-device collection error only for real failures.
2. A degraded array during recovery in `/proc/mdstat` (`[U_]` with a `recovery = 12.3%` line) → `raid.state = recovering` (not degraded), `devices_failed`, `sync_pct = 12.3`; a resync that is `PENDING`/`DELAYED`.
3. Device paths with odd characters from `--scan` (spaces, Windows `\\.\PhysicalDrive0`, `/dev/disk/by-id/…`) are passed as one argv element, and the item name passes the item contract.
4. Temperature sensor keys that repeat (two `coretemp_core_0` from two sockets) — items must not collide silently.
5. A host where smartctl hangs on one device — the others still report within the bounds.

---

### Task 1: Manager accepts the three plugins

**Files:** `internal/checks/definitions.go` (constants, `supportedPlugins`, `validateParams`), new `internal/checks/hardware_params.go` + test; `internal/rules/catalog.go` (+ OBLIGATION list) + test; `internal/api` enums and doc strings in `check_handlers.go`, `host_check_handlers.go`, `shared_handlers.go` (+ the enum-pinning test); `web/src/api/schema.d.ts` via `web/scripts/api-gen.sh`.

- [ ] Table tests for the three parameter shapes (accepted/refused per Global Constraints, incl. Windows device forms you pin here), catalogue rows, API create 201/400. RED → GREEN → lint → commit `feat(checks): accept the hardware plugins`.

### Task 2: `temperature` plugin

**Files:** `internal/agent/checks/plugins/temperature/` (`temperature.go`, tests), registry + `registry_test.go`.

- [ ] Fixture-provider tests: all sensors, `sensors` filter, high/critical only when non-zero, none → collection error, duplicate keys (Review Focus 4: disambiguate deterministically, e.g. suffix `#2`, and document it), item contract. RED → GREEN → lint (all three GOOS) → commit `feat(agent): report temperatures`.

### Task 3: `smart` plugin

**Files:** `internal/agent/checks/plugins/smart/` (runner seam over exec, JSON parsing, plugin, tests with recorded JSON fixtures under `testdata/`), registry.

- [ ] Tests for Review Focus 1, 3, 5 and every metric (ATA, NVMe, failing disk with exit bits set, standby, USB bridge, missing smartctl, `devices` filter, bounds with a fake runner that blocks). RED → GREEN → lint (three GOOS) → commit `feat(agent): report SMART health`.

### Task 4: `raid` plugin

**Files:** `internal/agent/checks/plugins/raid/` (mdstat parser, Linux reader, stub for other OSes, tests with `testdata/` mdstat samples), registry.

- [ ] Parser table tests (Review Focus 2 and every state; raid0/1/5/6/10; `arrays` filter; zero arrays → `raid.arrays = 0`), stub test. RED → GREEN → lint (three GOOS) → commit `feat(agent): report software RAID state`.

### Task 5: SPA

**Files:** plugin lists in `web/src/features/checks/CheckDialog.tsx` and `web/src/features/instance/shared/SharedCheckFormPage.tsx` with list fields for `sensors`/`devices`/`arrays` (same multi-line list as disk's mounts), `web/src/features/hosts/hostChecks.ts` (`NUMERIC_PLUGIN`, `DISCRETE_METRICS` += `raid.state`), i18n en/de, Vitest.

- [ ] Tests: each plugin selectable, list params round-trip (empty → `{}`), client validation mirrors Task 1, `raid.state` not graphable. RED → GREEN → `npm test`, `npm run lint`, `npx tsc -b` → commit `feat(web): offer the hardware checks`.

### Task 6: Documentation

**Files:** `docs/BACKLOG.md` (3.9 done; new row 3.9a "ZFS pool health" planned), `docs/upgrades.md`; noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-hardware-checks` from `docs/pulsix-public-url`: check-plugins reference (three plugins, params, metrics, platform notes, smartmontools install per platform), monitoring guide example rules, changelog. `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos.
