# Web Server Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Four agent check plugins — `nginx`, `apache`, `caddy`, `phpfpm` — that read a web server's own status page over HTTP on the host, with an optional basic-auth secret from `agent.yaml` (3.5).

**Architecture:** Each plugin is its own package under `internal/agent/checks/plugins/<name>/` exporting only `New()`, implementing `checks.StatefulPlugin` (rates) and `checks.SecretPlugin` on its instances, the way `redis` does. The HTTP request, its limits, the failure texts and the basic-auth secret live in a new shared package `internal/agent/checks/plugins/internal/webcheck`, built on `dbcheck` (Parse, Classify, Down, Counter, timeouts). Each plugin task is vertical: agent package, descriptor, golden tests, SPA fixture, `schema.d.ts`, i18n, upgrades paragraph — the "Adding a plugin" note in `internal/agent/checks/plugins/registry.go` is the checklist.

**Tech Stack:** Go (net/http, no new dependency; own Prometheus text parser for caddy), testcontainers, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-13-web-server-checks-design.md`

## Global Constraints

- Plugin names/check keys `nginx`, `apache`, `caddy`, `phpfpm`; SPA order after `redis`. Parameters, defaults, secret fields, metric names and kinds exactly as the spec's tables.
- Params: `url` (nginx/apache/caddy; string, format `statusURL`, MaxBytes 500, Default the spec's URL) or `urls` (phpfpm; list, ≤ 16 entries, each ≤ 500 bytes, format `statusURL`, ItemParam, `HostItemAlwaysAsked: true`); `secret` = `checkplugins.SecretParam()` optional (not Required); `verify_tls` bool Default `true`.
- `SecretErrorMetric()`: `nginx.up`, `apache.up`, `caddy.up`, `phpfpm.pools_down`.
- Secret fields `basic_auth_user` (required when a secret is named), `basic_auth_password`; unknown fields refused via `dbcheck.Parse`.
- HTTP: GET, `User-Agent: pulsix-agent`, `Transport.Proxy` nil, redirects not followed (`http.ErrUseLastResponse`), TLS ≥ 1.2, `InsecureSkipVerify` only when `verify_tls` is false, `DisableKeepAlives`, request ≤ `dbcheck.QueryTimeout` (5 s), round ≤ `dbcheck.CollectTimeout` (10 s), body ≤ 4 MiB.
- Failure texts exactly: `dbcheck.Classify` classes; `authentication failed (HTTP 401)`; `permission denied (HTTP 403)`; `HTTP status <code>`; `response too large`; `unexpected response format`; apache `not reported by the server (ExtendedStatus Off)`; caddy `HTTP metrics are not enabled in Caddy`. Never the URL, a header, the body, a Go error message, the user or the password. Tests plant a user and password and assert they never appear in any observation or advisory error.
- Caller context ending → return nothing but `ctx.Err()` (the Plugin contract); own deadline → `up` = 0 "timed out".
- Rates absent in the first round and after a counter reset (`dbcheck.Counter`); phpfpm also resets on a changed `start time`; rate state keyed by URL string.
- Every item passes `checks.ReportableItem`. No rule seeding.
- Integration tests use testcontainers images `nginx:1.27-alpine`, `httpd:2.4-alpine`, `caddy:2.8-alpine`, `php:8.3-fpm-alpine` (with an nginx container in front on a testcontainers network for the FPM status page); never touch any other Docker container on this machine.
- Repo rules: TDD; never `go test ./...` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …`); `golangci-lint run` and `GOOS=windows golangci-lint run ./internal/agent/...` clean; `GOOS=linux|darwin|windows go vet ./internal/agent/...`; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; public docs name no tech stack; never touch `~/_dev/noix-docs`; no state-changing git besides your own commits (no stash, reset, checkout, rebase, amend).

## Review Focus

1. A credential or URL reaching an observation, advisory error, log line or panic message — through a `*url.Error` (its text carries the URL), a TLS error naming the host, or a redirect's `Location`.
2. A server that accepts TCP but never answers, or trickles the body byte by byte — the round still ends within 10 s with `up` = 0 "timed out", the connection closed.
3. A hostile page: many MiB, a Prometheus line of megabytes, numbers that overflow uint64, negative or NaN values, a JSON page with wrong types — bounded, no panic, `unexpected response format`.
4. Counter resets (server restart, apache graceful, FPM reload) and the first round — no negative or huge rate.
5. The basic-auth header sent to the right place only: never without a secret, never after a redirect (none followed), never to a proxy from the environment.

---

### Task 1: `statusURL` format and the shared `webcheck` package

**Files:** `internal/checkplugins/formats.go` (+ test), `internal/checkplugins/testdata/formats.json`, `web/src/features/checks/paramFormats.ts` (+ the existing vector Vitest picks the new rows up); new `internal/agent/checks/plugins/internal/webcheck/` (+ tests).

**Produces (exact names):**
- `checkplugins.FormatStatusURL Format = "statusURL"`: parses as a URL, scheme `http` or `https`, non-empty host, no user info, no fragment (`#`), does not contain `{address}`. TS twin `statusURL` in `paramFormats.ts`. Vectors: the defaults of all four plugins valid; `ftp://x/`, `http:///x`, `http://u:p@h/`, `http://h/#f`, `http://{address}/`, `127.0.0.1/status`, empty, a control character invalid; `https://[::1]:8443/status?json` valid.
- `webcheck.MaxBody = 4 << 20`, `webcheck.UserAgent = "pulsix-agent"`
- `type webcheck.Auth struct { User, Password string; Set bool }`; `webcheck.ParseAuth(fields map[string]string) (Auth, error)` — nil fields → `Auth{}`; else `dbcheck.Parse` with `basic_auth_user` required and `basic_auth_password` optional.
- `type webcheck.Options struct { VerifyTLS bool; Auth Auth }`
- `webcheck.Fetch(ctx context.Context, rawURL string, opt Options) (body []byte, reason string)` — reason `""` on status 200 within limits, else one fixed text from the Global Constraints. Builds a fresh `http.Client` per call (no shared transport).
- `webcheck.WithQueryKey(rawURL, key string) string` — appends the bare key (`auto`, `json`) to the query when no query key of that name exists; leaves everything else unchanged.
- `webcheck.URLParam(params map[string]any, name, def string) string`, `webcheck.VerifyTLS(params map[string]any) bool` (default true).
- `webcheck.DownItem(item, prefix, up string, others []string, reason string) []checks.Observation` — `dbcheck.Down` for a given item (phpfpm's pools).

- [ ] Tests first: format vectors (Go and Vitest via formats.json); `Fetch` against `httptest` servers: 200 body returned; 301 with a `Location` → `HTTP status 301` and the target never requested; 401/403/500 texts; a body of 4 MiB + 1 → `response too large`; a handler that sleeps past 5 s → `timed out` within 6 s; a closed port → `connection refused`; a TLS server with `VerifyTLS` true → `TLS handshake failed`, false → body; `Authorization` header present exactly when `Auth.Set`, with the planted user/password, and the planted values absent from every reason; the client's transport has `Proxy == nil` (internal test); `User-Agent` sent. `ParseAuth`: nil, missing user, unknown field (e.g. `password` alone, as a Redis secret has) refused, password empty allowed. `WithQueryKey` table (no query, other keys, key present as `auto` and `auto=1`). RED → GREEN → lint (three GOOS) → `go test` checkplugins and webcheck → commit `feat(agent): add the status URL format and the shared web check helpers`.

### Task 2: `nginx`

**Files:** `internal/agent/checks/plugins/nginx/` (plugin, parser, unit and integration tests), registry + `registry_test.go` (incl. `TestRegistryStatefulPlugins`); `internal/checkplugins/nginx.go` + registry/golden tests; `internal/checks` constant + plugin-list test; `internal/rules/catalog_test.go` golden; SPA fixture; `web/src/api/schema.d.ts` (api-gen); i18n en/de (both picker maps, `checks.params.nginx.*` incl. the secret hint naming agent.yaml); `docs/upgrades.md` version-skew paragraph. Follow `redis` for the plugin shape (prototype + `NewInstance`, `Collect` delegating to `CollectWithSecret` with nil fields).

- [ ] Unit tests: the stub_status fixture → every metric; rates over two rounds, reset (counters drop) → absent; first round absent; a page missing a line or with a non-number → `unexpected response format`; every Fetch failure → `nginx.up` 0 and the seven others error observations with that text; caller context cancelled → `ctx.Err()` only; bad secret field → `up` 0 with the field text. Integration: `nginx:1.27-alpine` with a config enabling `stub_status` at `/nginx_status`, `up` 1 and active connections ≥ 1, second round has the rates. RED → GREEN → lint → tests (agent nginx/plugins, checkplugins, checks, rules, internal/api `-run` the enum/plugin tests) → commit `feat(agent): report nginx health`.

### Task 3: `apache`

**Files:** as Task 2 for `apache`.

- [ ] Unit tests: `?auto` fixtures for event and prefork, with and without `Total Accesses`/`Total kBytes` (ExtendedStatus Off → the two rates are error observations with the fixed text, the rest reported); `workers_busy_pct` incl. busy+idle = 0 → error observation `unexpected response format`; `auto` appended to the URL actually requested (default and a user URL without it); rates and reset; failures and secret as Task 2. Integration: `httpd:2.4-alpine` with `mod_status` loaded and `/server-status` allowed, `up` 1, busy ≥ 1. RED → GREEN → lint → tests → commit `feat(agent): report Apache httpd health`.

### Task 4: `caddy`

**Files:** as Task 2 for `caddy`, plus the package's Prometheus text parser (bounded: lines ≤ 64 KiB, `# ` lines skipped, label values with `\\`, `\"`, `\n` escapes, values parsed as float64, NaN/Inf/negative counters → that series ignored).

- [ ] Unit tests: parser table (labels, escapes, timestamps after the value, an over-long line → `unexpected response format`); metrics from a fixture with two servers and three handlers per server (requests per server = largest handler, summed), 5xx summed over codes 500–599 per (server, handler) then largest handler then summed; a fixture with only Go runtime and admin series → `caddy.up` 1 and the three others `HTTP metrics are not enabled in Caddy`; rates and reset; failures and secret as Task 2. Integration: `caddy:2.8-alpine` with a Caddyfile turning metrics on, the admin endpoint reachable from the test (`admin 0.0.0.0:2019` inside the container with `origins` as needed), one request served before the round, `up` 1. RED → GREEN → lint → tests → commit `feat(agent): report Caddy health`.

### Task 5: `phpfpm`

**Files:** as Task 2 for `phpfpm`; `urls` is the ItemParam with `HostItemAlwaysAsked: true`.

- [ ] Unit tests: JSON fixtures of PHP 7.4 and 8.3 → every per-pool metric on item = URL entry; `json` appended; two URLs fetched concurrently (one hangs → `timed out` on that item only, the other reported, round ≤ 10 s); duplicate entries reported once; empty list → the default URL as item; `phpfpm.pools_down` on item `""` every round (0, 1, count with a bad secret field); `processes_busy_pct` with total 0 → error observation; rates, reset on a lower counter and on a changed `start time`; rate state of a removed entry dropped, of a kept entry kept across a reorder; wrong JSON types → `unexpected response format`; caller cancel; planted secret absent. Integration: `php:8.3-fpm-alpine` with `pm.status_path = /status` and `nginx:1.27-alpine` in front on a testcontainers network, `up` 1 and `processes_total` ≥ 1. RED → GREEN → lint → tests → commit `feat(agent): report PHP-FPM pool health`.

### Task 6: SPA form tests

**Files:** `web/src/features/checks/CheckDialog.webserver.test.tsx`, `web/src/features/instance/shared/SharedCheckFormPage.webserver.test.tsx` (patterned on the `.database` tests).

- [ ] Tests: each of the four plugins selectable in both forms; `url` placeholder shows the default and is not sent when untouched; a `statusURL`-invalid URL refused client-side; `urls` list round-trip and edit prefill; `secret` optional with the agent.yaml hint; `verify_tls` sent only when changed. RED where possible → GREEN → `npm test`, `npm run lint`, `npx tsc -b` → commit `test(web): pin the web server check forms`.

### Task 7: Documentation

**Files:** `docs/BACKLOG.md` (3.11 done with a short note; new rows: PHP-FPM over FastCGI without a web server; nginx Plus API / Apache `MaxRequestWorkers` and per-vhost data / Caddy upstream health), the "Adding a plugin" note if a step was missing; noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-web-server-checks` from `docs/pulsix-database-checks`: check-plugins reference (four plugins, parameters, metrics, versions, how to switch on each status page with a config snippet restricted to localhost), agent configuration page (the two secret fields with an example), monitoring guide example rules, changelog. `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record the web server checks (backlog 3.11)`).
