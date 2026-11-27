# More web server data (backlog 3.11b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apache reports busy workers against the server's worker limit; nginx reads the nginx Plus API for per-zone and per-upstream items; Caddy reads its admin API for per-upstream health items. Existing metrics keep their meaning.

**Architecture:** All three stay single plugins built on `webcheck`. Apache gains two metrics from the `Scoreboard:` line it already fetches. nginx and caddy gain one optional URL parameter each (`api_url`, `upstreams_url`); without it a round is byte for byte what it was (the existing `webcheck.Collect` path). With it the round fetches the extra pages concurrently through `webcheck.Round` and adds discovered items (`zone:<name>`, `upstream:<name>`, `upstream:<address>`), bounded, rates from the previous sample per item. Descriptors gain the params and metrics, with a `MetricAsked` hook so the new item metrics are asked only while the parameter is set (the docker `resources` pattern).

**Tech Stack:** Go (stdlib, `encoding/json`; no new dependency), testcontainers (`httpd:2.4-alpine`, `caddy:2.8-alpine`), React/TypeScript (param texts only).

**Spec:** backlog row 3.11b (docs/BACKLOG.md); the 3.11 design `docs/superpowers/specs/2026-11-13-web-server-checks-design.md` for everything not changed here.

## Global Constraints

- No migration, no protocol change, no new dependency, no new format. A check without the new parameters behaves as before apart from apache's two added metrics.
- Failure texts are the existing fixed strings (`webcheck` reasons, `unexpected response format`) plus the new ones below; never a URL, header, body, user or password. The API pages' JSON error bodies are never read into a text.
- Every item passes `checks.ReportableItem`; discovered items are capped and the rest counted.
- A context that ends returns only `ctx.Err()` and moves no baseline (the `webcheck.Round` contract).

## Rulings

- R1 Apache's limit is the `Scoreboard:` line's length, reported as `apache.workers_max`, with `apache.workers_busy_max_pct` = busy ÷ that × 100. — Measured against `httpd:2.4-alpine` (2.4.71) and `httpd:2.4.38-alpine` on 2026-11-26: the `?auto` scoreboard has exactly the effective `MaxRequestWorkers` slots (mod_status prints max daemons × threads per child, not `ServerLimit × ThreadLimit` as the 3.11 spec assumed): event defaults 400, event `MaxRequestWorkers 150` → 150 (also with `ServerLimit 32`), event `160` with `ThreadsPerChild 25` → 150 (Apache's own rounding), event `ThreadLimit/ThreadsPerChild 100`, `MaxRequestWorkers 300` → 300, worker `100` → 100, prefork `50` → 50, prefork default 256. So no operator parameter is needed and the figure is "against the limit". The new integration test pins it on Linux CI. — If wrong for some MPM or build: the percentage reads low; the absolute `workers_busy` is unaffected.
- R2 `apache.workers_busy_pct` keeps its meaning (against started workers); the limit-based figure is a new metric. Value not clamped: busy above the slots (a graceful restart) reads above 100. A missing `Scoreboard:` line reads "not reported by the server (no Scoreboard)" on both new metrics, an empty one or one with a character outside `_SRWKDCLGI.` reads `unexpected response format` on both; the page itself still reads (old pages and tests stay valid). The new metrics are asked always (no parameter switches them).
- R3 Apache per-vhost data is infeasible and stays out: `?auto` has no vhost at all; the ExtendedStatus HTML table names the vhost of each worker's current or last request — a snapshot of at most one request per slot, not per-vhost counts or rates — and it would mean scraping an HTML table of up to `MaxRequestWorkers` rows whose layout is not a contract. Per-vhost traffic needs the access log (a different feature). Recorded in the backlog row.
- R4 nginx Plus: new optional parameter `api_url` (format `statusURL`, ≤ 500 bytes, no default, example `http://127.0.0.1/api/9`), the API base including its version. The agent reads `<api_url>/http/server_zones` and `<api_url>/http/upstreams` (`url.JoinPath`; a query is kept) in the same round as `stub_status`, concurrently. `url` stays the `stub_status` page with its default: the existing metrics keep their source (Plus ships `stub_status` too). — If wrong: a Plus user without `stub_status` sees `nginx.up` 0; reading host metrics from the API when `url` is empty is additive later.
- R5 nginx items: `zone:<name>` with `nginx.zone_processing`, `nginx.zone_requests_per_s`, `nginx.zone_responses_4xx_per_s`, `nginx.zone_responses_5xx_per_s`, `nginx.zone_received_bytes_per_s`, `nginx.zone_sent_bytes_per_s`; `upstream:<name>` with `nginx.upstream_peers`, `nginx.upstream_peers_up` (state `up`), `nginx.upstream_peers_unavailable` (state `unavail` or `unhealthy`; `down`, `draining`, `checking` are neither), `nginx.upstream_active`, `nginx.upstream_requests_per_s`, `nginx.upstream_responses_5xx_per_s`, `nginx.upstream_fails_per_s` (peer sums). Item `""` gets `nginx.api_up` (1 both pages read and parsed, else 0) and `nginx.api_items_unreported` (zones and upstreams over the cap or refused as an item). No per-peer items: "N of M peers up" is the alertable figure, per-peer items multiply tuples (peers × metrics) and churn with DNS-resolved peers. — If wrong: per-peer items are additive later.
- R6 Caps: at most 64 zones and 64 upstreams (nginx), 64 upstreams (caddy), first by name (byte order). Worst case per check: nginx 64×6 + 64×7 + 2 = 834 tuples, caddy 64×3 + 3 = 195, well under the host's 4,096 (`checks.MaxTuplesPerHost`).
- R7 Rates per item from the previous sample (`dbcheck.Counter`, absent in an item's first round and after a fall). An upstream whose peer set changes (ids and servers) restarts its three rates, since its sums are not continuous then. State of an item no longer on the page is dropped.
- R8 When an API page fails (fetch, status, size, JSON), `<p>.api_up`/`caddy.admin_up` = 0 with the fixed reason on the item-`""` siblings (`dbcheck.Down`), and every item the instance reported before gets the reason on each of its metrics, so a rule on an item shows why rather than going stale; baselines are kept. `stub_status`/metrics-page failures stay as today and do not touch the API items (except caddy's health, R10).
- R9 Caddy: new optional parameter `upstreams_url` (format `statusURL`, ≤ 500 bytes, no default, example `http://127.0.0.1:2019/reverse_proxy/upstreams`), read in the same round as the metrics page. Items `upstream:<address>` with `caddy.upstream_requests` (`num_requests`: requests in flight to it), `caddy.upstream_fails` (`fails`: failures currently counted, within `fail_duration`), `caddy.upstream_healthy` (1/0). Item `""` gets `caddy.admin_up`, `caddy.upstreams_unhealthy` (count of reported upstreams reading 0), `caddy.upstreams_unreported`. A negative count from the page reads 0.
- R10 Caddy health comes from the metrics page's `caddy_reverse_proxy_upstreams_healthy{upstream="<address>"}` gauge, joined by address — measured on 2026-11-26 against Caddy 2.8.4 and 2.11.7: the admin list has only `address`, `num_requests`, `fails`; the gauge is on the metrics page with or without the HTTP metrics option, includes active and passive health, and turned 0 for a dead upstream. An upstream without the series reads "not reported by the server" on `caddy.upstream_healthy`; a failed metrics page puts its reason there (and `caddy.upstreams_unhealthy` gets it too). A gauge line whose labels do not parse is ignored, never a page error, so no page that reads today stops reading.
- R11 The optional basic-auth secret is sent to every page of the check (as phpfpm sends it to every pool): one operator wrote all URLs of one check.
- R12 Version skew: an older agent ignores `api_url`/`upstreams_url` in a pushed check (the docker `resources` precedent) and the item metrics never appear — nothing reads wrong, nothing is read in their place; it refuses a local `agent.yaml` check that sets them (unknown parameter). An older agent never reports apache's two new metrics; they are asked always, so a rule on them reads no data until the agent is updated. An older manager refuses the parameters on save. Documented in `docs/upgrades.md`.
- R13 New metrics are asked only while their parameter is set (`MetricAsked`, like docker's): nginx's zone/upstream/api metrics with a non-empty `api_url`, caddy's upstream/admin metrics with a non-empty `upstreams_url`. Removing the parameter makes their current values read "you stopped asking" at once.

## Review Focus

- An API page that is not the API (HTML, `null`, an array, wrong types, negative or fractional counts, 4 MiB+) — `unexpected response format` / `response too large`, `api_up` 0, no panic: `TestNginxAPIRejectsMalformedPages`, `TestCaddyUpstreamsRejectsMalformedPages`.
- The cap and item contract: 70 zones → 64 items, 6 unreported; a name with a control character skipped and counted — `TestNginxAPICapsItems`, `TestCaddyUpstreamsCapsItems`.
- A failing API after a good round keeps the reason visible on the remembered items — `TestNginxAPIDownMarksKnownItems`, `TestCaddyAdminDownMarksKnownItems`.
- No param, no change: the round fetches exactly the one page and reports exactly the old metrics — `TestNginxWithoutAPIURLReadsOnlyStubStatus`, `TestCaddyWithoutUpstreamsURLReadsOnlyMetrics`.
- Secrets and URLs never in a text, with the API pages answering 401/500 with a body that names them — `TestNginxAPINeverLeaks`, `TestCaddyAdminNeverLeaks`.
- Apache limit read correctly from real servers — `TestAgainstARealServer` (apache, `MaxRequestWorkers 150` → `workers_max` 150).

---

### Task 1: Apache worker limit

**Files:** `internal/agent/checks/plugins/apache/{parse.go,apache.go,parse_test.go,apache_test.go,integration_test.go}`, `internal/checkplugins/apache.go`, `internal/rules/catalog_test.go`.

`status` gains `slots uint64; slotsReason string`. Tests: `TestParseScoreboard` (table: length, absent, empty, bad character, first line wins), `TestWorkersAgainstTheLimit`, `TestDownCoversTheNewMetrics`; integration with `MaxRequestWorkers 150`. Commit `feat(apache): report busy workers against the server's worker limit`.

### Task 2: nginx Plus API

**Files:** create `internal/agent/checks/plugins/nginx/{api.go,api_test.go}`; modify `nginx.go`, `internal/agent/checks/plugins/internal/webcheck/webchecktest/webchecktest.go` (`FindItem`), `internal/checkplugins/nginx.go` (+ test of `MetricAsked`), `internal/rules/catalog_test.go`.

```go
type apiZones map[string]zone       // parseZones(body) (apiZones, error)
type apiUpstreams map[string]upstream // parseUpstreams(body) (apiUpstreams, error)
func apiURLs(base string) (zones, upstreams string, ok bool)
```
Tests as in Review Focus plus `TestNginxAPIZonesAndUpstreams` (values and rates over two rounds), `TestNginxAPIPeerSetChangeRestartsRates`. Commit `feat(nginx): per-zone and per-upstream items from the nginx Plus API`.

### Task 3: Caddy upstreams

**Files:** create `internal/agent/checks/plugins/caddy/{upstreams.go,upstreams_test.go}`; modify `caddy.go`, `parse.go` (healthy gauge), `integration_test.go` (reverse proxy to a dead upstream, admin API), `internal/checkplugins/caddy.go`, `internal/rules/catalog_test.go`.

Tests as in Review Focus plus `TestParseHealthyGauge`, `TestCaddyUpstreamHealthJoin`. Commit `feat(caddy): per-upstream health from the admin API`.

### Task 4: SPA texts and fixture

**Files:** `web/src/i18n/{en,de}/checks.ts` (`checks.params.nginx.api_url`, `checks.params.caddy.upstreams_url`), `web/src/test/checkPlugins.json` (regenerated), `schema.d.ts` regenerated if it changes. Commit `feat(web): texts for the web server API parameters`.

### Task 5: Docs

`docs/BACKLOG.md` row 3.11b → done; `docs/upgrades.md` skew paragraph. Commit `docs: backlog 3.11b done`.
