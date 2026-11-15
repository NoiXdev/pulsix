# Probe vantage points (backlog 3.13a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Synthetic checks are probed from the manager and from agents of monitoring groups, with per-vantage results and a fail policy whose verdict the check's existing metrics carry.

**Architecture:** Four vantage params on every synthetic descriptor; the probe runner computes agent assignments (rendezvous hash) into `probe_assignments`, agents fetch them over the existing stream and send `ProbeResults`, all vantages land in `probe_current`/`probe_samples`, and the runner combines them per fail policy into the check's normal tuples.

**Tech Stack:** Go, PostgreSQL/TimescaleDB, sqlc, protobuf/gRPC, React/TypeScript SPA.

**Spec:** `docs/superpowers/specs/2026-11-16-probe-vantages-design.md`

## Global Constraints

- A check without `vantage_groups` behaves exactly as today: no per-vantage writes, no assignment rows, identical recorded observations.
- Migration 00098 only, additive; sqlc regenerated with the repo's make target; `docs/upgrades.md` rules apply.
- Proto changes additive (new field numbers only); regenerate with the repo's generator.
- Limits verbatim: ≤ 8 groups, `agents_per_group` 1–10 default 3, 200 assignments per agent, 4 concurrent agent probes, freshness 2 × interval + 30 s, policy formulas as the spec's Combining step 4.
- Repo English; public docs never name the tech stack; no secret value in errors or logs.
- Never `go test ./...`; per package `go test -race -timeout 900s` (`internal/api` `-timeout 1800s`); `golangci-lint run` clean; for agent code also `GOOS=windows golangci-lint run ./internal/agent/...` and `GOOS=linux|darwin|windows go vet ./internal/agent/...`.
- Conventional Commits; footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; commit on the branch only; no push, tag, stash or other state-changing git; touch no Docker container you did not start (testcontainers are fine).

## Review Focus

1. An agent sending results for a slot it is not (or no longer) assigned, or a metric of another plugin — must be dropped, never recorded (Task 4).
2. Assignment churn: one agent going offline must move only its own slots; the target host never probes itself; the 200 cap (Task 3).
3. Manager-only checks unchanged — a regression test comparing recorded observations before/after for a check without groups (Task 3).
4. Freshness and `n = 0`: an agent silently gone must stop counting after 2 × interval + 30 s, and with no fresh vantage the check turns unknown, not up (Task 3).
5. Agent safety: guard refusals, concurrency bound, unknown plugin or bad params → errors, not a crash or a goroutine leak on config replacement (Task 5).

---

### Task 1: Vantage parameters, validation and group protection

**Files:** `internal/checkplugins/vantage.go` (+ test), the six synthetic descriptors, `formats.go` (`FormatUUID`), goldens per the "Adding a plugin" note (params only; no new plugin), `internal/checks` (create/update refuse unknown groups; `DeleteGroup` in-use refusal with keys), `internal/api` (409 mapping like the secret in-use case), SPA fixture regeneration, `web/scripts/api-gen.sh`, SPA format twin for `FormatUUID`, en/de param texts (dialog layout comes in Task 6).

**Interfaces — Produces:** `checkplugins.VantageOf(params map[string]any) Vantage` with `type Vantage struct { Manager bool; Groups []uuid.UUID; PerGroup int; Policy string }` (defaults applied), and `checkplugins.StripVantage(params) map[string]any` (copy without the four params) — used by Tasks 3 and 4.

- [ ] Failing tests: params on every synthetic descriptor and none on agent ones; hook cases from the spec; `VantageOf` defaults; service refusals; API 409 with keys.
- [ ] Implement; PASS `internal/checkplugins`, `internal/checks`, `internal/rules`, WHOLE `internal/api`; SPA vitest, lint, `tsc -b`.
- [ ] Commit `feat(checks): vantage point parameters on synthetic checks`.

### Task 2: Storage

**Files:** `internal/db/migrations/00098_probe_vantages.sql` (`probe_assignments`, `probe_current`, `probe_samples` hypertable with retention, `host_agents.probes_supported`, RLS and grants like 00096), queries + sqlc output, a store in `internal/probes` (`store_sql.go`) with integration tests through `internal/db/testdb`.

**Interfaces — Produces** (in `internal/probes`): `type Assignment struct { OrgID, AgentHostID, TargetHostID uuid.UUID; CheckKey, Plugin string; Params map[string]any; Interval time.Duration; Address string }`; store methods `ReplaceOrgAssignments(ctx, org, []Assignment) error` (diff), `AgentAssignments(ctx, org, agentHost) ([]Assignment, error)`, `RecordVantage(ctx, org, vantage string, obs []checks.Observation) error` (current upsert + samples), `VantageCurrent(ctx, org, host, key) ([]VantageRow, error)`, `DeleteVantages(ctx, org, host, key, keep []string) error`; agents store `SetProbesSupported(ctx, host, bool)` and a listing of online capable agents with host ids.

- [ ] Failing integration tests (migration up/down, RLS isolation between orgs, diff replace, upsert/samples, delete-keep).
- [ ] Implement; PASS `internal/db/...`, `internal/probes`, `internal/agents`.
- [ ] Commit `feat(db): probe assignments and per-vantage results`.

### Task 3: Runner — assignment and combining

**Files:** `internal/probes` (`assign.go`, `combine.go`, runner changes), `internal/app` wiring.

- Assignment per the spec's "Assignment" (rendezvous hash, exclusions, cap 200, dedupe across groups), written per org on refresh via `ReplaceOrgAssignments`; group membership through the checks service's group expansion.
- Combining per the spec's "Combining" steps 1–5; manager-only checks keep the current path untouched; unexpected vantages' rows deleted on refresh.

- [ ] Failing tests per the spec's Tests (assignment, combining) and Review Focus 2–4, with fakes for the store.
- [ ] Implement; PASS `internal/probes`, `internal/app`.
- [ ] Commit `feat(probes): probe synthetic checks from agent vantage points`.

### Task 4: Agent protocol — manager side

**Files:** `proto/pulsix/agent/v1/agent.proto` + generated code, `internal/agentapi` (heartbeat fields → `SetProbesSupported` on change; `ProbeConfig` on the ack on version mismatch for capable agents; `ProbeResults` ingest with the spec's validation; budget and limits as check results), version encoding function shared with the agent (put it in a small package both may import, e.g. `internal/probeconfig`).

- [ ] Failing tests per the spec (ack only on mismatch, only capable agents, unassigned slot and foreign metric dropped and counted, limits) and Review Focus 1.
- [ ] Implement; PASS `internal/agentapi`, `internal/grpcsrv` if touched, `internal/pb/...`.
- [ ] Commit `feat(agentapi): send probe assignments to agents and take their results`.

### Task 5: Agent side

**Files:** `internal/agent/probes/` (scheduler, executor with `internal/probe/all` and `probe.NewGuard(nil, nil)`, concurrency 4), agent loop wiring (heartbeat `probes_supported` true and `probe_config_version`; apply `ProbeConfig`; send `ProbeResults` through the existing send path and replay ring in chunks of 200).

- [ ] Failing tests per the spec's Agent bullets and Review Focus 5 (config replacement stops old slots without leaks; `-race`).
- [ ] Implement; PASS `internal/agent/...`; lint/vet for the three GOOS.
- [ ] Commit `feat(agent): probe other hosts on the manager's assignment`.

### Task 6: API and SPA

**Files:** host checks handler + OpenAPI (`vantages`, `fail_policy`), samples endpoint `vantage` parameter, `web/scripts/api-gen.sh`, SPA checks dialog (group multi-select, conditional fields, secret hint), host page vantage table and graph vantage selector, en/de texts.

- [ ] Failing API tests and SPA tests per the spec's API and SPA section.
- [ ] Implement; PASS WHOLE `internal/api`; SPA vitest, lint, `tsc -b`.
- [ ] Commit `feat(web): vantage points on synthetic checks`.

### Task 7: Documentation

`docs/BACKLOG.md` (3.13a done with a summary; new planned rows: zone-worker vantages, confirmation by immediate re-probe, rules per vantage), `docs/upgrades.md` (migration 00098, version skew); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-probe-vantages` from `docs/pulsix-zfs-pools`: synthetic checks / check-plugins reference (vantage points, fail policy, secret rule, agent load and network position), changelog. Build (251 pages before). Commit in both repos; push nothing.
