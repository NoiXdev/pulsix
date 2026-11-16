# Web assertions and failure reasons (backlog 3.13b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Synthetic checks report why they failed; `http` reports a timing breakdown, size, redirects and certificate data and evaluates per-check assertions with a severity each; alerts and notifications carry the reason or the failed assertion.

**Architecture:** New metrics on the synthetic descriptors (`<plugin>.failure`, the http breakdown, `http.assertion`/`http.assertion_actual` per assertion item); a parser shared by the descriptor hook and the probe; seeded rules (migration 00099); a detail captured by the alerts executor through a port and carried to the API and notifications.

**Tech Stack:** Go (`net/http/httptrace`, `regexp`, `golang.org/x/net/html`, new `github.com/andybalholm/cascadia`), PostgreSQL, React/TypeScript SPA.

**Spec:** `docs/superpowers/specs/2026-11-16-web-assertions-design.md`

## Global Constraints

- Values verbatim from the spec: failure vocabulary table; 32 assertions, 512 bytes per entry, 256 bytes per regex or selector; 1 MiB kept body, 16 MiB counted; 128 bytes per actual/issuer label; 512 bytes per alert detail; the seven seeded rule names, rules and severities.
- No error text from a library leaves a probe (failure labels only); `Set-Cookie` actual is `(hidden)`; no secret value in errors or logs.
- Migration 00104 only, additive (seed rows + `alerts.detail`, `alert_events.detail`); sqlc regenerated with the repo's target; `docs/upgrades.md` rules apply. No proto change.
- Repo English; public docs never name the tech stack.
- Never `go test ./...`; per package `go test -race -timeout 900s` (`internal/api` `-timeout 1800s`); `golangci-lint run` clean; for code under `internal/probe` or `internal/agent` also `GOOS=windows golangci-lint run ./internal/probe/... ./internal/agent/...` and `GOOS=linux|darwin|windows go vet` on the same.
- Every descriptor change follows the "Adding a plugin" note in `internal/agent/checks/plugins/registry.go` (goldens, catalogue table, SPA fixture `-update-spa-fixture`, `web/scripts/api-gen.sh`, en/de texts).
- Conventional Commits; footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; commit on the branch only; no push, tag, stash or other state-changing git; touch no Docker container you did not start (testcontainers are fine).

## Review Focus

1. A library error text (URL with a token, an internal address) reaching an observation, a label or an alert detail (Tasks 1, 3, 5).
2. A down http check with assertions opening the host-scope unknown alert at once instead of the `http.up` rule's crit alert (Task 3: down → no assertion observations).
3. Hostile site content in `assertion_actual`, `cert_issuer` or the detail reaching chat markup or control characters (Tasks 3, 5: `probe.Label`, `agentText`).
4. Body handling: a 16 MiB+ body, a slow body under the timeout, truncation rules for negative ops, HTML/JSON parsing bounded by the gate (Tasks 2, 3).
5. The detail lookup failing must not abort the alerts transaction (savepoint) and a severity action finds its subject (Task 5).

---

### Task 1: Failure reasons and `tls.cert_issuer`

**Files:** `internal/probe/params.go` (`Down(plugin, reason string)`), a classifier in `internal/probe` (`failure.go` + tests) for net/dns/context/tls/x509 errors, each probe package (`icmp`, `tcp`, `http`, `tls`, `dns`, `snmp`) mapping its failures (dns rcodes `nxdomain`/`servfail`; snmp `auth`; http `protocol`/`redirects`/`certificate`), `FailAll` lists including `<plugin>.failure`, the six descriptors (`<plugin>.failure` discrete; `tls.cert_issuer` discrete), goldens and catalogue per the note, SPA fixture/schema, en/de metric names and failure-label texts.

**Interfaces — Produces:** `probe.Down(plugin, reason string) []probe.Observation`; `probe.Failure*` label constants; `probe.IssuerLabel(*x509.Certificate) string` (used by Task 2).

- [ ] Failing tests: classifier table; per plugin the labels of the spec's table with loopback servers or fakes (refused, timeout, reset, tls, certificate, protocol, redirects, nxdomain, servfail, auth); `none` on success; guard refusal stays an error on every metric including `<plugin>.failure`; issuer rule (CN, else O, else DN; cleaned; 128 bytes).
- [ ] Implement; PASS `internal/probe/...`, `internal/checkplugins`, `internal/checks`, `internal/rules`, `internal/probes`, `internal/agentapi`, WHOLE `internal/api`; SPA vitest, lint, `tsc -b`; lint/vet for the three GOOS.
- [ ] Commit `feat(probe): report why a synthetic check failed`.

### Task 2: http breakdown, size, redirects, certificate

**Files:** `internal/probe/http` (httptrace timing summed over hops, redirect count, body read to 16 MiB counting with the first 1 MiB kept when needed, final-hop leaf for `cert_expiry_days`/`cert_issuer`), `FailAll` and the descriptor's metrics, goldens/catalogue/SPA fixture, en/de texts.

**Interfaces — Produces:** an internal response record the assertion evaluator reads (status, timings, redirects, content bytes, whether the kept body was truncated, kept body, final headers, leaf certificate) — name and shape the implementer's, documented in the package.

- [ ] Failing tests per the spec's Tests "Breakdown" bullet and Review Focus 4 (16 MiB error on `content_bytes` only, slow body under the timeout, HEAD, IP literal without `dns_ms`, http without `tls_ms`/cert metrics, redirect sums).
- [ ] Implement; PASS as Task 1's list.
- [ ] Commit `feat(probe): time each phase of an http check`.

### Task 3: Assertions

**Files:** a parser package used by both sides without an import cycle (e.g. `internal/probe/assert` with `Parse(entry string) (Assertion, error)` — `internal/checkplugins` may import it if it imports nothing of checkplugins; otherwise place it under `internal/checkplugins/assertion` and let `internal/probe/http` import it), `FormatAssertion` + the http hook (duplicates across `json_values`, HEAD with body subjects, limits) + `ItemAsked` covering assertion names, the evaluator in `internal/probe/http` (html via `golang.org/x/net/html` + cascadia in a gate slot; json via the existing JSONPath), `FailAll` assertion items, `go.mod`/`go.sum` (cascadia), SPA format twin if the repo keeps one for list formats, goldens/fixture, en/de param texts.

**Interfaces — Produces:** `Assertion` with its subject, op, operand, severity and canonical `Expression() string` (the text without name and severity — Task 5 shows it).

- [ ] Failing tests per the spec's Tests "Assertion parser" and "Assertion evaluation" bullets and Review Focus 2, 3 (down attempt → no assertion observations; FailAll → errors on both metrics of every item; actual cleaned and cut; `Set-Cookie` hidden).
- [ ] Implement; PASS as Task 1's list plus `internal/agent/...` (binary still builds; note its size change in the report).
- [ ] Commit `feat(probe): assertions on http checks`.

### Task 4: Migration 00104 and seeded rules

**Files:** `internal/db/migrations/00104_web_assertions.sql` (the seven rules' backfill between the `rules-seed-backfill` markers, like 00097; `alerts.detail text`, `alert_events.detail text`), `internal/rules/seed.go`, the seed tests modelled on 00097's (`TestRulesBackfillMatchesSeedRules` coverage, a per-org comparison test), sqlc regeneration.

- [ ] Failing tests: backfill equals `SeedRules()`; per-org rows equal `SeedForOrg`; an org holding a rule of one of the names keeps it and gets no duplicate; migration down removes only seed rows of the seven names.
- [ ] Implement; PASS `internal/db/...`, `internal/rules`, `internal/checks`, WHOLE `internal/api` (seed counts in API tests may move).
- [ ] Commit `feat(rules): seed down and assertion rules for synthetic checks`.

### Task 5: Alert detail

**Files:** `internal/alerts` (port `DetailSource`, capture on `ActionOpen`/`ActionSeverity` inside a savepoint, subject from the open alerts for severity actions, `Detail` on `Alert`/views/timeline events, store and queries), `internal/app` (the port over checks current values and the host's resolved definitions: `<plugin>.up` → `<plugin>.failure` when not `none`; `http.assertion` → `<expression> — saw <actual>`; 512-byte cut), `internal/alertroutes` (`AlertItem.Detail` from the event; escalation from the alert's latest detail), `internal/notify` (render after the subject, `agentText`; sample and hostile sample events), `internal/api` alert handlers + OpenAPI, `web/scripts/api-gen.sh`.

- [ ] Failing tests per the spec's section 4 and Review Focus 1, 3, 5 (lookup error leaves the transaction usable; close/flap events carry none; escalation uses the alert's detail; email and chat render; webhook `alerts.items | json` carries `detail`).
- [ ] Implement; PASS `internal/alerts`, `internal/alertroutes`, `internal/notify`, `internal/app`, `internal/rules`, WHOLE `internal/api`.
- [ ] Commit `feat(alerts): say why an alert fired`.

### Task 6: SPA

**Files:** checks dialog assertion editor for `assertions` (rows: name, subject + argument, operator filtered by subject, operand hidden for `exists`, severity; text toggle; per-row API errors), host page (`http` breakdown stacked bar, assertion table, translated failure labels next to `<plugin>.up`), alert row / host alerts card / timeline showing `detail`, en/de texts.

- [ ] Failing vitest tests for the editor (compose and parse canonical text, operator filtering, text toggle round trip), the breakdown bar, the assertion table, failure label translation, alert detail rendering.
- [ ] Implement; PASS SPA `npx vitest run`, `npm run lint`, `npx tsc -b`.
- [ ] Commit `feat(web): assertion editor, timing breakdown and alert reasons`.

### Task 7: Documentation

`docs/BACKLOG.md` (3.13b done with a summary; 3.13f noted as delivered; new planned rows: other request methods and custom request headers/bodies; multi-step flows; screenshots/JavaScript rendering; assertions on other plugins), `docs/upgrades.md` (migration 00099 incl. the immediate crit alerts for failing checks after upgrade; dependency cascadia; skew note); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-web-assertions` from `docs/pulsix-probe-vantages`: check-plugins reference (failure reasons, new http/tls metrics, assertions with grammar and examples, seeded rules), alerts/notifications pages (detail), changelog. Build (251 pages before; run `npm ci` first if `node_modules` is missing). Commit in both repos; push nothing.
