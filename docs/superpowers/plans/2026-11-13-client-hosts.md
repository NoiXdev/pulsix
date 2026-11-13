# Client Hosts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A host can be a server or a client; a client whose agent is off is frozen (no unknown, no host alert, alerts kept), judged normally once back, with an optional "not seen for N days" warning.

**Architecture:** `hosts.kind` (additive migration) through the hosts service/API/SPA; `org_monitoring_settings.client_absent_days` (new org-scoped table) with service/API/SPA; the rules loader passes kind, agent `last_seen` and `online_since` into the snapshot; `rules.Evaluate` freezes an offline client and anchors staleness at `max(reading, online_since)` for an online one; `alerts.Reconcile` opens/closes the absent warning (host-scope, severity `warn`) and closes an unknown host alert as stale when a host freezes.

**Tech Stack:** Go, PostgreSQL migrations + sqlc (v1.31.1 binary at `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/bin/sqlc`, `<path> generate` from the repo root), huma OpenAPI + `cd web && PATH=$PWD/node_modules/.bin:$PATH bash scripts/api-gen.sh`, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-13-client-hosts-design.md`

## Global Constraints

- Values: `kind` ∈ {`server`, `client`}, default `server`; `client_absent_days` integer 0–365, default 14, 0 = off; online = `agents.isOnline` (`last_seen` within `agents.StaleAfter`); absent warning = host-scope alert, severity `warn`; frozen-host close of an unknown host alert = `stale`.
- Migrations additive, next free numbers after the newest in `internal/db/migrations` (check; the correctness bundle added 00088/00089); follow `docs/upgrades.md`; the new table copies `org_job_settings`' RLS policies (00033).
- `internal/rules` never imports `internal/checks` or `internal/agents`; pass what it needs through its ports/Load (an `online` boolean computed by the store with `agents.StaleAfter` passed in, or a duration constant passed as an argument — no copy of the value without a pinning test).
- Layering per CLAUDE.md (handlers → services → stores; SPA `src/ui` presentational).
- Repo rules: TDD; never `go test ./...` or `make test` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …` plus ONE whole internal/api run in each task that touches it); `golangci-lint run` clean; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never touch Docker containers other than testcontainers tests start; tests never dial default ports; no state-changing git besides your own commits; temp files only in the scratchpad.

## Review Focus

1. A server host behaves exactly as before in every path (Evaluate, Reconcile, due times) — the change is gated on kind = client with an agent.
2. A frozen client never writes a state change, a `stale` close of a tuple alert, or a notification; buffered results arriving on reconnect are judged after unfreeze, not lost.
3. On reconnect a long-interval tuple read before the outage is not unknown until `online_since` + 3×interval; a NULL `online_since` falls back to the reading time (today's rule).
4. Absent warning: opens once after the threshold, never while online, closes resolved on return, setting changes take effect at the next evaluation; it does not collide with an unknown host alert (same subject).
5. Kind changes mark the host dirty; server→client while off closes an open unknown host alert as stale; client→server while off goes unknown normally.

---

### Task 1: Host kind and the org monitoring setting (backend)

**Files:** migration `000NN_host_kind.sql` (`hosts.kind` + CHECK) and `000NN_org_monitoring_settings.sql`; `internal/db/queries/hosts.sql` (create/update/read carry kind), new `org_monitoring_settings.sql` queries; sqlc regen; `internal/hosts` service/store (validation: invalid kind → invalid input), a small settings service (where org job settings live today — follow that pattern); `internal/api` host DTOs (`kind`), settings endpoint (read/update `client_absent_days`, admin permission as the job settings use); apitest tests; `schema.d.ts`. Marking the host dirty for evaluation on a kind change (find how a host update marks evaluation dirty today, e.g. `MarkHostDirty`).

- [ ] Tests first: store round-trip and CHECK; service rejects bad kind and out-of-range days; apitest host create/update/read with kind (default server), 422 on bad kind; settings GET default 14 without a row, PUT 0/365 ok, 366/-1 → 422, cross-org isolation; kind change marks dirty. RED → GREEN → sqlc → api-gen → lint → tests (`./internal/hosts/...`, settings package, internal/api `-run 'Host|Setting'` then the whole internal/api once) → commit `feat(hosts): mark a host as server or client and add the client absent setting`.

### Task 2: Freeze and the staleness anchor (rules)

**Files:** `internal/rules/load_sql.go` (+ the query feeding it: host kind, agent last_seen/online_since, absent days), `internal/rules/ticker.go` (`HostLoad`), `internal/rules/evaluate.go` (`Snapshot` gains e.g. `Client bool`, `AgentOnline bool`, `AgentOnlineSince time.Time`, `AgentLastSeen time.Time`, `AbsentAfter time.Duration`; frozen path; `staleAt` anchor; `nextDue` absent deadline), tests (pure Evaluate tables + DB-backed Load).

**Produces:** the snapshot fields above and a `Result` flag Task 3 reads (e.g. `Result.Frozen bool`, `Result.Absent bool` with the absent-since time) — name them in the report.

- [ ] Tests first: frozen client → no state changes, roll-up unchanged, nothing unmeasured/closed, next due = absent deadline or silent heartbeat; online client with tuple read before `online_since` not stale until `online_since`+3×interval; NULL/zero connected → today's rule; server and client-without-agent unchanged (table rows mirroring existing tests); Load fills the fields from the DB. RED → GREEN → lint → `./internal/rules/...`, `./internal/app/...` → commit `feat(rules): freeze a client host while its agent is off`.

### Task 3: Absent warning and alert handling

**Files:** `internal/alerts/reconcile.go` (host action: frozen → no unknown open; frozen with open unknown host alert → close stale; absent → open host alert severity warn; online again → close resolved), the notification text path for a host-scope warn alert (find where host-alert notification texts are built: "not seen since …"), alerts tests, any API alert DTO text if it is built server-side.

- [ ] Tests first: each transition above; server path unchanged; absent setting 0 never opens; notification text for the absent warning; no duplicate open across ticks. RED → GREEN → lint → `./internal/alerts/...`, `./internal/notify/...`, `./internal/rules/...`, internal/api `-run Alert` → commit `feat(alerts): warn when a client host has not been seen for days`.

### Task 4: SPA

**Files:** `web/src/features/hosts/HostForm.tsx`/`HostDialog.tsx` (kind select + no-agent hint), `HostsListPage.tsx`/`HostRow.tsx`, the fleet overview component, `HostDetailPage.tsx`/`HostStateSection.tsx` (Off badge, muted last roll-up, "Client" tag), the alerts list/detail wording for a host-scope warn alert, the org settings page (absent days field), i18n en/de (de: "Server", "Client", "Aus", "seit … nicht gesehen", "Warnung nach … Tagen ohne Kontakt (0 = aus)"), Vitest.

- [ ] Tests first: form sends kind; hint shows for client without agent; Off badge for an offline client in list/fleet/host page, not for servers or online clients; alert wording; settings field bounds. RED → GREEN → `npm test`, `npm run lint`, `npx tsc -b` → commit `feat(web): server and client hosts, the off badge and the absent setting`.

### Task 5: Documentation

**Files:** `docs/BACKLOG.md` (new row 3.57 "Client hosts" done with summary; 5.5 note that availability will count only servers; "Last updated"), `docs/upgrades.md` (migrations; behaviour: none until a host is marked client); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-client-hosts` from `docs/pulsix-check-correctness`: hosts page, monitoring page, organization settings, changelog; `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record client hosts (backlog 3.57)`, `docs(pulsix): server and client hosts`).
