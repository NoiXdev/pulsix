# Plugin Descriptors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One descriptor per check plugin in a leaf package `internal/checkplugins`, from which the manager's validation, item logic, rules catalogue and API enums derive, and which the SPA renders its check form from — with no behaviour change.

**Architecture:** Leaf package (no Pulsix imports) with one file per plugin and a registry. `internal/checks`, `internal/rules`, `internal/api` derive from it. A new `GET /api/v1/check-plugins` serves the descriptors; the SPA renders fields generically. Named formats exist in Go and TypeScript with a shared test-vector file pinning parity.

**Tech Stack:** Go (huma v2), React/TypeScript, Vitest.

**Spec:** `docs/superpowers/specs/2026-11-12-plugin-descriptors-design.md`

## Global Constraints

- No behaviour change: every existing validation test (internal/checks `*_params_test.go`, `definitions_test.go`, rules catalogue tests, API tests, openapi enum test, SPA tests) keeps passing — move tests, don't weaken them. Metric names, kinds and check keys identical; API routes and request/response shapes identical except the one new route; `schema.d.ts` enums identical in content.
- Leaf package `internal/checkplugins` imports nothing from `github.com/noixdev/pulsix/...` (a test may).
- Plugins (15): cpu, memory, disk, service, network, uptime, docker, temperature, smart, raid, icmp, tcp, http, tls, dns — plus any other `supportedPlugins` entry found; order as the SPA shows today.
- New route `GET /api/v1/check-plugins`, authenticated (any signed-in user), static.
- Formats implemented in Go and TS; parity via `internal/checkplugins/testdata/formats.json` read by a Go test and a Vitest test (Vitest imports it by relative path from `web/` — or copy-at-generate if the build forbids it; decide in Task 4 and say how).
- Agent code untouched; a cross-check test: agent `plugins.Registry()` names == descriptors with `Runs = Agent`.
- Repo rules: TDD where behaviour is added (route, renderer, derivations); for moves the existing tests are the net. Never `go test ./...` (single packages, `-race -timeout 900s`; internal/api `-timeout 1800s -run …`); `golangci-lint run` clean; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never touch the unrelated Docker containers here.

## Review Focus

1. A parameter rule that lived in code and has no declarative twin (http's `{address}` substitution, tcp `send`, dns `server`, docker label split at `=`, smart `..` segments) silently weakened in the move.
2. `AsksForItem`'s special cases (item `""` asked for the hardware plugins, smart `<path> [<type>]`) preserved.
3. The OpenAPI document and `schema.d.ts` unchanged in content for existing routes (enum order and values).
4. SPA: untouched fields still not sent; defaults still shown as placeholders/preselection; tcp escapes; the server marker; list → `{}` when empty; edit prefill round-trips for every plugin.
5. i18n completeness enforced by a test, not by convention.

---

### Task 1: `internal/checkplugins` with all descriptors, the generic validator and formats

**Files:** create `internal/checkplugins/` (`descriptor.go` types + registry, `validate.go`, `formats.go`, one file per plugin, `testdata/formats.json`, tests). Port every rule from `internal/checks/{definitions,synthetic_params,docker_params,hardware_params,items}.go` into descriptors + formats + minimal `Check` hooks.

- [ ] Port the existing accept/refuse tables into `checkplugins` tests (they must pass against the new validator), plus the formats vector test and an item-param test mirroring `AsksForItem`'s cases. Commit `refactor(checks): describe every check plugin in one place`.

### Task 2: Manager derives from the descriptors

**Files:** `internal/checks` (supportedPlugins, IsSynthetic, validateParams, itemParam/AsksForItem, error plugin list) — delete the `*_params.go` files whose rules now live in `checkplugins`; `internal/rules/catalog.go` builds `Catalogue()` from descriptors; the agent cross-check test.

- [ ] All existing internal/checks, internal/rules, internal/items tests pass unchanged in meaning; lint. Commit `refactor(checks): derive validation, items and the rules catalogue from the descriptors`.

### Task 3: API enums from the registry, and `GET /check-plugins`

**Files:** `internal/api` (computed enum for the plugin fields of the three DTOs, generated params doc strings, new handler + DTO + apitest test), `web/src/api/schema.d.ts` regenerated.

- [ ] Test: the route returns every descriptor with params/constraints/formats/metrics; unauthenticated → 401; the openapi enum test unchanged; schema.d.ts diff limited to the new route. Commit `feat(api): serve the check plugin descriptors`.

### Task 4: SPA renders the check form from the descriptors

**Files:** a hook in `web/src/api/hooks`, a generic param-field renderer + client validator + TS formats in `web/src/features/checks/` (replacing `syntheticParams.ts`/`hardwareParams.ts`/`SyntheticParamFields.tsx`/`HardwareListField.tsx` and the per-plugin branches in `CheckDialog.tsx`/`SharedCheckFormPage.tsx`), `hostChecks.ts` NUMERIC/DISCRETE derived, i18n keyed by plugin+param, the formats vector Vitest, the i18n completeness test.

- [ ] Existing Vitest suites for both forms (docker, hardware, synthetic, disk/network/service) keep passing (adapt only mock setup for the new hook). Commit `refactor(web): build the check form from the plugin descriptors`.

### Task 5: Documentation

**Files:** `docs/BACKLOG.md` (3.54 done), the "Adding a plugin" note in `internal/agent/checks/plugins/registry.go`, the `internal/checkplugins` package doc.

- [ ] Edit and commit `docs: record the plugin descriptors (backlog 3.54)`.
