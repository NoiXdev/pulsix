# Forgetting Items Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Forget an item `(host, check_key, item)` that is gone — automatically after 7 quiet days while the host keeps reporting, and at once from the host page — removing its current values, rule states and state-change history and closing its open alerts with reason `forgotten`, while samples stay.

**Architecture:** The alerts package gains the `forgotten` close reason and a transactional close for one item's open alerts. The checks domain gains `ForgetItem` and an expiry pass, both deleting in one transaction per item and closing alerts through a port into the alerts service. A manager loop under its own advisory lock runs the expiry hourly. The API exposes `DELETE …/hosts/{host}/items`; the SPA offers **Forget** on stale or no-longer-asked item rows.

**Tech Stack:** Go (pgx, sqlc, huma), Postgres migration, React/TypeScript SPA.

**Spec:** `docs/superpowers/specs/2026-11-12-forget-items-design.md`

## Global Constraints

- Forget removes, for every metric of the item: `check_current`, `check_states`, `check_state_changes` rows; closes every open alert on the item with `closed_reason = 'forgotten'` plus an alert event; notification routing treats it like a resolve. `check_samples` and item decisions are untouched.
- All of one item's deletes and alert closes happen in ONE transaction.
- Expiry rule: item's newest reading (max `at` over its metrics) older than `now - period` AND the host has some reading newer than `item_newest + period`. Applies to item `""` too. Offline hosts are never touched.
- Period: default `168h`; `PULSIX_ITEM_EXPIRY` (Go duration, ≥ `1h`, `0` disables); invalid values refuse startup with a clear message.
- Loop: hourly, advisory lock key `8315` (next free after 8314), bounded per round (≤ 5000 items, batches), panic recovered and logged with stack, one INFO log per round with per-organization counts. No per-item audit for automatic forgets.
- API: `DELETE /api/v1/orgs/{org}/hosts/{host}/items?check_key=…&item=…` (item may be empty; present-but-empty means `""`), 204 on success, 404 when nothing matched, role = whatever may write item decisions; audit entry with host, check key, item, actor.
- Migration additive: only widens the alerts `closed_reason` CHECK to include `forgotten`; next number after 00084.
- Repo rules: TDD; never `go test ./...` (single packages with `-race -timeout 900s`, `internal/api` with `-timeout 1800s` and `-run`); `golangci-lint run` clean; layers per CLAUDE.md (no SQL in handlers or services); commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; public docs name no tech stack; never touch the Docker engine on this machine; never touch ~/_dev/noix-docs (use the scratch clone).

## Review Focus

1. Ingest racing a forget: a reading for the item arriving during the forget transaction either lands before (and is removed) or after (and re-creates the item cleanly, with no orphan rule state) — a store test with two connections.
2. A host offline for longer than the period keeps every current value (expiry test with an offline host).
3. An item with several metrics where only one is old: the item's newest reading decides, not per metric (test).
4. The tuple cap: after forgetting, the host's tuple count drops and new tuples are accepted again (test through the checks store's cap path).
5. A forgotten item with an open, acknowledged or snoozed alert: the alert closes with `forgotten`, its routes get the resolve-like event once, and a re-appearing item opens a fresh alert episode rather than reopening the closed one.

---

### Task 1: Alerts can close an item's alerts as `forgotten`

**Files:** new migration `internal/db/migrations/00085_alert_closed_reason_forgotten.sql`; `internal/db/queries/alerts.sql` (+ sqlc regen); `internal/alerts/alert.go` (`Forgotten CloseReason = "forgotten"`); `internal/alerts/service.go` / `store_sql.go` (a method closing every open alert of `(org, host, check_key, item)` inside a caller's `pgx.Tx`, writing the alert events and whatever notification bookkeeping a resolve writes); tests.

**Interfaces — produces:** `func (s *Service) CloseForgotten(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID, checkKey, item string, now time.Time) (int, error)` (count closed). Mirror how `Apply`/`Sweep` close alerts; reuse their event and notify path rather than writing a new one.

- [ ] Failing store/service tests (Review Focus 5's alert half), RED, implement, GREEN, lint, commit `feat(alerts): close an item's alerts as forgotten`.

### Task 2: The checks domain forgets an item and finds expired ones

**Files:** `internal/db/queries/checks.sql` (+ sqlc); `internal/checks/forget.go`, `forget_test.go`, store methods in `store_sql.go` + store tests.

**Interfaces — consumes:** Task 1's `CloseForgotten` through a port interface declared in `internal/checks` (e.g. `type AlertCloser interface { CloseForgotten(ctx, tx, org, host, key, item, now) (int, error) }`). **Produces:**
- `func (s *Service) ForgetItem(ctx context.Context, orgID, hostID uuid.UUID, checkKey, item string) error` (`ErrNotFound` when no `check_current` row matched);
- `func (s *Service) ForgetExpired(ctx context.Context, period time.Duration, limit int) (map[uuid.UUID]int, error)` (forgotten count per org; runs across organizations — check how other cross-org loops get past row-level security, e.g. the probes runner or `alerts.Sweep`, and do the same).

- [ ] Failing tests: delete of all three tables for one item only (siblings untouched), samples kept, ErrNotFound, Review Focus 1–4, expiry boundaries (exactly at the period, offline host, `""` item), limit honoured. RED, implement, GREEN, lint, commit `feat(checks): forget an item and expire quiet ones`.

### Task 3: Manager loop and setting

**Files:** `internal/config/config.go` (+ test) for `PULSIX_ITEM_EXPIRY`; `internal/checks/expiry_loop.go` (or next to the other loops' style) + test; `internal/app/app.go` wiring with `schedule.NewAdvisoryLocker(pool, 8315)` under a named constant; `startLoop`.

- [ ] Failing tests: config parsing (default 168h, `0` disables, `30m` refused, garbage refused); loop runs a round per tick only when holding the lock, logs per-org counts, recovers a panic, does nothing when disabled. RED, implement, GREEN, lint, commit `feat(manager): expire quiet items hourly`.

### Task 4: API and GUI

**Files:** `internal/api` (new handler + route + apitest test, audit entry, schema regen with `web/scripts/api-gen.sh`); `web/src/api/hooks` (mutation hook invalidating the host's checks query); the host page's item row component (find where stale badges render: `web/src/features/hosts/HostChecksCard.tsx` and `hostChecks.ts`); i18n en/de (button, confirm title/body as in the spec, toast); Vitest.

- [ ] Failing tests: API 204/404/403 (role), empty item via `item=`, audit row written; SPA shows **Forget** only for stale or not-asked rows, confirm text, calls DELETE with the right query, refreshes. RED, implement, GREEN; `npm test`, `npm run lint`, `npx tsc -b`; Go lint; commit `feat(hosts): forget a gone item from the host page`.

### Task 5: Documentation

**Files:** `docs/BACKLOG.md` (3.12b → done with a short note), `docs/upgrades.md` (setting, new close reason, first-round effect), noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-forget-items` from `docs/pulsix-docker-checks`: host page Forget action, automatic expiry and its setting (admin/config reference page where other `PULSIX_*` settings are listed), the `forgotten` alert reason (wherever alert close reasons are described), and in the docker check docs drop the tuple-cap argument for labeled mode (keep labeled mode as a noise reducer). `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos.
