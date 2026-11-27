# Web server checks: nginx, Apache, Caddy, PHP-FPM (backlog 3.11) — design

Status: the owner accepted the proposal on 2026-11-13 ("passt weiter"):
four agent plugins reading the web server's own status page on the host —
nginx `stub_status`, Apache `mod_status` (`?auto`), Caddy's metrics
endpoint, PHP-FPM's pool status page (one item per pool) — with the URL set
in the GUI, a localhost default per plugin, an optional `agent.yaml` secret
for HTTP basic authentication, numbers only, no redirects, bounded replies,
10 s per round, other hosts allowed, PHP-FPM over FastCGI directly left for
later. Every other decision below is **(Claude's call)**.

## Intent

Next to the databases (3.10), the web server is what most hosts run. Its
own status page says whether it is up and how loaded it is, which neither
the synthetic `http` check (one URL from the outside) nor the host's CPU
shows. Zabbix agent 2 and Checkmk read the same pages.

## The plugins

Names and check keys: `nginx`, `apache`, `caddy`, `phpfpm`, in the SPA's
order after `redis`. Agent plugins, Linux, macOS and Windows. A host may
run several checks of one plugin (two nginx instances): each check has its
own key.

### Parameters (GUI)

| Plugin | Parameter | Type | Default | Notes |
|---|---|---|---|---|
| nginx | `url` | string, format `statusURL`, ≤ 500 bytes | `http://127.0.0.1/nginx_status` | |
| apache | `url` | same | `http://127.0.0.1/server-status?auto` | the agent adds the query key `auto` when it is missing |
| caddy | `url` | same | `http://127.0.0.1:2019/metrics` | the admin endpoint; a `metrics` handler on a site works as well |
| phpfpm | `urls` | list of `statusURL`, ≤ 16 entries, each ≤ 500 bytes | one entry, `http://127.0.0.1/status` | the ItemParam; each entry is one pool; the agent adds the query key `json` when it is missing |
| all four | `secret` | `checkplugins.SecretParam()`, optional | none | basic authentication, see below |
| all four | `verify_tls` | bool | `true` | for an `https` URL; `false` accepts any certificate |

New format **`statusURL`** (Go and TypeScript, vectors in
`internal/checkplugins/testdata/formats.json`): an `http://` or `https://`
URL with a host, no user info, no fragment, no `{address}` placeholder (the
agent fetches it as written; it is not a server-side probe). Absent
parameters mean the defaults; an empty `urls` list means the one default
URL. (Noted during implementation: a list parameter cannot carry a
`Default` — the API types a default as a string, a number or a boolean —
so phpfpm's descriptor gives the default URL as the `urls` Example, and the
GUI hint states it.)

### Secret fields (optional secret)

| Field | Meaning |
|---|---|
| `basic_auth_user` | required when a secret is named |
| `basic_auth_password` | may be empty |

Unknown fields are refused (`dbcheck.Parse`), so a secret written for
another plugin — a Redis secret with only `password`, a PostgreSQL secret
with `host`/`user`/`password` — is refused rather than sent as basic
authentication to whatever URL the check names. The field names are
distinct from every database plugin's for this reason. Without a secret no
`Authorization` header is sent. Who may create checks (the operator role,
`checks.write`) may also run jobs on the host (`jobs.run`), so pointing a
check at another host adds no new reach; the distinct fields keep a check
from carrying a database password to it.

### The HTTP request

One shared helper (new package
`internal/agent/checks/plugins/internal/webcheck`, using `dbcheck` for
field parsing, error classification and rates):

- `GET`, header `User-Agent: pulsix-agent`, basic authentication from the
  secret when named.
- No proxy (the environment's proxy variables are ignored: status pages are
  local, and credentials must not go to a proxy), redirects not followed
  (a 3xx reads as its status), TLS 1.2 or newer, verification per
  `verify_tls`, no connection reuse (one connection per request, closed).
- Each request at most 5 s (`dbcheck.QueryTimeout`), one round at most
  10 s (`dbcheck.CollectTimeout`); phpfpm fetches its URLs concurrently
  under the round's deadline.
- The body is read up to 4 MiB; a larger one reads "response too large"
  (changed during implementation: phpfpm reads at most 1 MiB per pool, so
  16 pools stay within 16 MiB; a `full` page of about 2,500 processes fits).
- Status 200 is required. 401 reads "authentication failed (HTTP 401)",
  403 "permission denied (HTTP 403)", any other "HTTP status <code>".
- A body that is not the expected format reads "unexpected response
  format".
- Network failures are classified by `dbcheck.Classify` (`connection
  refused`, `timed out`, `host not found`, `TLS handshake failed`,
  `connection failed`). **Error texts never carry the URL, a header, the
  body or a Go error message**; nothing from the page is reported except
  the numbers below.

### Metrics

Item `""` unless stated. When the page cannot be fetched or parsed,
`<p>.up` = 0 and every other metric of that item gets an error observation
with the reason (`dbcheck.Down`; phpfpm per pool item). Rates (`*_per_s`)
are absent in the first round and after a counter reset
(`dbcheck.Counter`), so nginx, apache, caddy and phpfpm are
`checks.StatefulPlugin`s (per-key clone; the instances also implement
`checks.SecretPlugin`). `SecretErrorMetric()` is `<p>.up` for nginx,
apache and caddy and `phpfpm.pools_down` for phpfpm: the scheduler reports
a missing secret on item `""`, and phpfpm's pools are items of their own,
so phpfpm reports `phpfpm.pools_down` on item `""` every round — which a
fixed secret then overwrites, instead of leaving a stale error behind. A
secret whose fields are refused (unknown field, no user) is a reason like
any other: `up` = 0 with that text.

**nginx** (`stub_status`, any nginx version that has the module)

| Metric | Kind | Value |
|---|---|---|
| `nginx.up` | numeric | 1 fetched and parsed, 0 not |
| `nginx.connections_active` | numeric | `Active connections` |
| `nginx.connections_reading` | numeric | `Reading` |
| `nginx.connections_writing` | numeric | `Writing` |
| `nginx.connections_waiting` | numeric | `Waiting` |
| `nginx.requests_per_s` | numeric | rate of `requests` |
| `nginx.connections_accepted_per_s` | numeric | rate of `accepts` |
| `nginx.connections_dropped_per_s` | numeric | rate of `accepts − handled` |

**apache** (Apache httpd 2.4, `mod_status` `?auto`)

| Metric | Kind | Value |
|---|---|---|
| `apache.up` | numeric | 1/0 |
| `apache.workers_busy` | numeric | `BusyWorkers` |
| `apache.workers_idle` | numeric | `IdleWorkers` |
| `apache.workers_busy_pct` | numeric | busy ÷ (busy + idle) × 100; 100 means no idle worker is left and new requests wait |
| `apache.requests_per_s` | numeric | rate of `Total Accesses` |
| `apache.bytes_per_s` | numeric | rate of `Total kBytes` × 1024 |

`Total Accesses` and `Total kBytes` exist only with `ExtendedStatus On`
(the default since 2.3.6); without them the two rates are error
observations "not reported by the server (ExtendedStatus Off)". The
percentage is against started workers, not `MaxRequestWorkers`, which the
page does not state (the proposal said "against the maximum"; the
scoreboard's slot count is `ServerLimit × ThreadLimit` and overstates it).
(Corrected by backlog 3.11b: measured on Apache 2.4.38 and 2.4.71, the `?auto`
scoreboard has exactly the effective `MaxRequestWorkers` slots;
`apache.workers_max` and `apache.workers_busy_max_pct` now report it.)

**caddy** (Caddy 2.6+, Prometheus text format)

| Metric | Kind | Value |
|---|---|---|
| `caddy.up` | numeric | 1 fetched and parsed, 0 not |
| `caddy.requests_in_flight` | numeric | `caddy_http_requests_in_flight` |
| `caddy.requests_per_s` | numeric | rate of `caddy_http_requests_total` |
| `caddy.errors_5xx_per_s` | numeric | rate of `caddy_http_request_duration_seconds_count` with a `code` label of 500–599 |

Caddy instruments every middleware handler, so one request is counted
once per handler it passes. Per `server` label the plugin takes the
**largest** value across `handler` labels (the outermost handler sees
every request), then sums the servers. For the 5xx rate the codes
500–599 are summed within each (server, handler) pair first, then the
largest handler per server is taken, then the servers are summed. Request metrics exist only when
they are switched on in Caddy (`servers { metrics }` up to 2.8, the `metrics` global option from 2.9); without any
`caddy_http_` series, `caddy.up` = 1 and the three others are error
observations "HTTP metrics are not enabled in Caddy". The parser reads the
text format line by line (comments skipped, at most 64 KiB per line,
labels parsed with escapes), ignores every other family, and needs no new
dependency.

**phpfpm** (PHP-FPM 7.4+, status page `?json`); item = the URL entry as
written in `urls` (the default URL when the list is empty)

| Metric | Kind | Value |
|---|---|---|
| `phpfpm.pools_down` | numeric | item `""`: how many listed pools have `phpfpm.up` = 0 this round |
| `phpfpm.up` | numeric | 1/0 per pool |
| `phpfpm.processes_active` | numeric | `active processes` |
| `phpfpm.processes_idle` | numeric | `idle processes` |
| `phpfpm.processes_total` | numeric | `total processes` |
| `phpfpm.processes_busy_pct` | numeric | active ÷ total × 100 (as apache: against started processes; `pm.max_children` is not on the page) |
| `phpfpm.listen_queue` | numeric | `listen queue` |
| `phpfpm.requests_per_s` | numeric | rate of `accepted conn` |
| `phpfpm.max_children_reached_per_s` | numeric | rate of `max children reached` |
| `phpfpm.slow_requests_per_s` | numeric | rate of `slow requests` |

A changed `start time` resets the pool's rate baselines. Rate state is
kept per URL string (changed during implementation: the agent's scheduler
starts a fresh plugin instance whenever a check's parameters change, so no
rate baseline survives an edit of `urls`; the per-URL state only matters
within one configuration). A URL listed twice is fetched and reported
once. The pool's name
from the page is not reported (the URL is the item, so the list in the GUI
and the items agree for AsksForItem). An entry the item contract refuses
cannot occur (500 bytes, `statusURL` has no control characters).

## Manager and SPA

One descriptor per plugin in `internal/checkplugins`, constants, golden
tests, rules catalogue, SPA fixture, `schema.d.ts`, i18n en/de (both picker
maps, `checks.params.<plugin>.*`), all per the "Adding a plugin" note in
`internal/agent/checks/plugins/registry.go`. phpfpm's `urls` is its
ItemParam with `HostItemAlwaysAsked` (item `""` carries
`phpfpm.pools_down`). The TypeScript twin of `statusURL`
and its vectors. No rule seeding.

## Tests

- Unit tests per plugin against `httptest` servers: every metric from
  fixture pages (nginx stub_status, apache `?auto` with and without
  ExtendedStatus and for prefork/event, Caddy metrics with several
  handlers and servers, with and without HTTP metrics, PHP-FPM JSON of
  7.4 and 8.x), rates across rounds and resets, every failure text
  (refused, timeout on a server that never answers, 301 not followed, 401,
  403, 500, oversized body, wrong format), basic auth sent exactly when a
  secret is named, a proxy in the environment ignored, a planted user and
  password never in any observation or advisory error, `auto`/`json` added
  to the query, secret field parsing (unknown field, missing user).
- Integration tests against real servers started by testcontainers:
  `nginx:1.27-alpine` with a mounted config enabling `stub_status`,
  `httpd:2.4-alpine` with `mod_status`, `caddy:2.8-alpine` with metrics on,
  `php:8.3-fpm-alpine` behind the nginx container or with its status page
  served by a small nginx sidecar — the builder picks the simplest setup
  that reaches the real FPM status page. `up` = 1 and plausible values.
- `statusURL` vectors pass in Go and TypeScript.
- SPA: the four plugins selectable in both forms, defaults shown, `urls`
  list round-trip, the secret hint.

## Documentation

- `docs/BACKLOG.md`: 3.11 done; new row for PHP-FPM over FastCGI directly
  (socket, no web server in between) and for nginx Plus / Apache
  `MaxRequestWorkers` if wanted.
- `docs/upgrades.md`: version-skew paragraph for the four plugins.
- noix-docs (scratch clone, new branch `docs/pulsix-web-server-checks` from
  `docs/pulsix-database-checks`): check-plugins reference (four plugins,
  parameters, metrics, how to enable each status page with a config
  snippet and restricting it to localhost), the agent configuration page
  (the two secret fields), monitoring examples, changelog.

## Non-goals

- PHP-FPM over FastCGI without a web server; nginx Plus API; Apache
  per-vhost data; Caddy upstream health from the admin API.
- Following redirects; client certificates; custom CAs (the system roots
  or `verify_tls` off).
- Reporting anything from a page but numbers.
