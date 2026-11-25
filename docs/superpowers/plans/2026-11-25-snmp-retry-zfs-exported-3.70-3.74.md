# SNMP v3 retry after discovery, zfs pools that may be exported (backlog 3.70, 3.74) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 3.70 and 3.74 (`grep -nE "^\| (3\.70|3\.74) " docs/BACKLOG.md`).

**Architecture:** 3.70 is in `internal/probe/snmp` (`snmp.go` first request, `client.go` `classifyFirst`) plus the failure vocabulary in `internal/probe/failure.go` and the SPA's failure-label texts. 3.74 is in the agent's zfs plugin (`internal/agent/checks/plugins/zfs`), its manager descriptor (`internal/checkplugins/zfs.go`, `items.go`, `descriptor.go`) and the evaluation that decides whether a metric of an item is still asked (`internal/app/rules_ports.go`, `internal/rules`).

**Tech Stack:** Go, React/TypeScript (labels, check form), docs.

**Spec:** the backlog rows plus the owner's decisions of 2026-11-25 (below); for zfs also `docs/superpowers/specs/2026-11-15-zfs-pools-design.md`.

## Owner decisions (2026-11-25, binding)
- **3.70:** one more attempt before judging; if the answer still does not come, the message names both possible causes (wrong privacy password, or the request was lost).
- **3.74:** a new zfs parameter `optional_pools`. A pool listed there that is absent from `zpool list` reports `exported` (fine, no alert). A pool listed in `pools` that is absent reports `missing` (an alert with the documented example rule) instead of nothing as today. Pools in neither list behave as today.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; SPA `cd web && npm run lint && npx vitest run <files>` plus `npx tsc -b` if types change; regenerate the API client the repo's way if the OpenAPI changes (`grep -n openapi Makefile`).
- No migration expected; if one becomes necessary, additive only, next free number. Proto changes additive only (none expected).
- Timestamps compared in tests are truncated to microseconds.
- TDD: the failing test first, shown failing for the right reason. Deterministic tests (fakes, recorded tool output; no sleeps as synchronisation).
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **3.70 retry.** When the session's first `get` fails and the failure is the *inferred* credentials case (`classify(err) == failDown`, v3, the device answered the discovery — today's `classifyFirst` rule), the poll sends the same first request once more (one more `c.get(first)`, which carries gosnmp's own `retries`) before judging. If that answers, the poll continues normally (round trip from the answering request). If it fails again the same inferred way, the poll reports a **new failure label** `auth_or_lost` (add to `internal/probe/failure.go`'s vocabulary in spec order right after `auth`; check every consumer of `FailureLabels()` / `FailureAuth`: rule catalogue labels, API enums, SPA label texts in `web/src/i18n/*/checks.ts` (en and de), docs-facing strings) with SPA text EN `credentials refused, or the requests were lost` / DE in the repo's existing German register for that file. A refusal gosnmp names explicitly (`ErrUnknownUsername`, `ErrWrongDigest`, `ErrUnknownSecurityLevel`, `ErrDecryption`) stays `auth` with no extra attempt. A retry whose failure is something else (a socket failure, the poll's deadline) is reported as that. The extra attempt must respect the poll's context deadline (no attempt once ctx is done). Update `classifyFirst`'s doc comment (the "for that one poll" sentence) and add a note in `docs/upgrades.md` "Behaviour changes to check after an upgrade" (a new failure label a rule on SNMP failure labels may want to include). Tests in `poll_internal_test.go` with the table client: (a) first request lost after discovery, second answers → up, values reported; (b) both lost → `auth_or_lost`; (c) an explicit `ErrWrongDigest` → `auth`, exactly one get; (d) v2c timeout → down/timeout as today, one get.
- **3.74 agent side.** `optional_pools` is a list parameter like `pools` (same format `FormatNoControl`, same validation). The agent plugin reads both. Selection: with both lists empty, every imported pool (today). Otherwise the reported pools are the imported pools named in either list — and when `pools` is empty but `optional_pools` is not, every imported pool plus the optional ones (an optional list does not narrow). A pool in `optional_pools` absent from `zpool list` reports exactly one observation: `zfs.state` = `EXPORTED`. A pool in `pools` (and not in `optional_pools`; a name in both counts as optional) absent from `zpool list` reports exactly `zfs.state` = `MISSING`. Labels are upper case like zpool's own. `zfs.pools` keeps counting only imported reported pools (say so in the package doc). Neither absent pool calls `zpool status` or `zfs get` for that name. A failing `zpool list` stays a host error with no per-pool observations (nothing is known to be absent). The scrub memory of an exported pool is kept (it may come back). Package doc comment updated. Tests with recorded `zpool list` output: optional absent → EXPORTED only; listed absent → MISSING only; both lists, both present → normal; name in both lists absent → EXPORTED; `zpool list` failure → host error only.
- **3.74 manager side: an absent pool's other metrics are not stale.** An EXPORTED or MISSING pool reports only `zfs.state`, and its other per-pool metrics must neither turn stale nor leave the host unknown nor open host-scope alerts; their last values read as not asked (like 3.28's "you stopped asking"), and when the pool is imported again everything reports normally. Implement this as a **generic descriptor hook** in `internal/checkplugins` (e.g. `MetricAskedForItem(params, item, currentLabels map[string]string, metric) bool`, or the shape that fits the evaluation's data best — find where `asksMetric`/`AsksForItem` are consulted in `internal/app/rules_ports.go` and `internal/rules`), so `smart`'s standby state (backlog 3.9b) can reuse it later; the zfs descriptor's hook says: while the item's current `zfs.state` is `EXPORTED` or `MISSING`, no other zfs metric of that item is asked. The `zfs.state` tuple itself is always asked. Add `optional_pools` to the descriptor's params and make `AsksForItem` for zfs accept items named in either list (an `ItemAsked` hook; keep `HostItemAlwaysAsked`). Check the host page / items page show a not-asked value the way they do for 3.28 and that nothing in the SPA breaks on the new parameter (the check form renders params from the descriptor — verify, add the param's label/help text in en and de if the form needs it). Tests: rules-level (or app-level) test that a pool with state EXPORTED and an old `zfs.capacity_pct` reading leaves the host OK, not unknown; the same with MISSING leaves capacity not asked and a rule `zfs.state not ONLINE` fires on MISSING; once state is ONLINE again a stale capacity reading counts as stale again.
- **Docs.** The zfs example rule in the user docs becomes "`zfs.state` is not `ONLINE` and not `EXPORTED`" (check the rule form can express a discrete comparison against several labels — `buildLabel` — and phrase the docs accordingly). `docs/upgrades.md`: a listed pool that does not exist now reports `MISSING` instead of nothing (an existing rule "not ONLINE" fires on it).

---

## Task 1: 3.70 one more attempt after discovery
**Files:** `internal/probe/snmp/snmp.go`, `client.go`, `poll_internal_test.go`, `internal/probe/failure.go` (+ its tests and every consumer of the vocabulary), `web/src/i18n/{en,de}/checks.ts`, `docs/upgrades.md`.
- [ ] Commit `fix(snmp): one more request before a lost answer reads as refused credentials`.

## Task 2: 3.74 agent: optional_pools, EXPORTED and MISSING
**Files:** `internal/agent/checks/plugins/zfs/*` (+ tests).
- [ ] Commit `feat(zfs): report exported optional pools and missing listed pools`.

## Task 3: 3.74 manager: the parameter and an absent pool's metrics not asked
**Files:** `internal/checkplugins/{zfs,descriptor,items}.go` (+ tests), `internal/app/rules_ports.go` and/or `internal/rules/*`, SPA check form texts if needed, `docs/upgrades.md`.
- [ ] Commit `feat(checks): an exported or missing pool's other metrics are not asked`.

## Task 4: Final
Whole-branch review, `make test`, lint, SPA lint/tests if touched, `docs/upgrades.md`, user docs in the docs clone (SNMP failure label; zfs `optional_pools`, EXPORTED/MISSING, example rule; changelog), backlog rows done, merge to local main, redeploy.
