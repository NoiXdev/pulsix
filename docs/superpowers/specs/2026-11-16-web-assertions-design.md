# Web assertions and failure reasons (backlog 3.13b, with 3.13f) — design

Status: the owner accepted the proposal on 2026-11-16 ("passt"):

- every synthetic check reports why an attempt failed, as a fixed
  vocabulary, never free text;
- the `http` check reports a timing breakdown, the redirect count, the
  content size and, for https, the certificate's expiry and issuer (the
  issuer also on `tls`);
- the `http` check gains `assertions`, `name=expression [@warn]`, each
  reporting `pass`, `warn` or `crit` and the value it saw, judged by two
  seeded rules;
- an alert on `<plugin>.up` carries the failure reason, an alert on an
  assertion its expression and the value seen — in the alert view and in
  notifications, as of the event;
- the host page shows the breakdown and an assertion table; the checks
  dialog has an assertion editor;
- (asked after the builder check, answered "Ja, seeden") the starter
  rules gain `<plugin>.up < 1` (crit) for `icmp`, `tcp`, `http`, `tls`
  and `dns`, so a down synthetic check alerts at once, with its reason;
- new dependency `github.com/andybalholm/cascadia` (dropped in review:
  html selectors are a subset Pulsix matches itself, section 3).

Split out as new backlog rows: requests other than GET/HEAD and custom
request headers or bodies; multi-step flows; screenshots or JavaScript
rendering; assertions on plugins other than `http`. Every decision below
not listed here is **(Claude's call)**.

## Goals

- A failed probe says why (3.13f).
- A web check can state what a good response is, in one place, with a
  severity per statement, and an alert says which statement failed and
  what was seen.
- Slow responses can be told apart: name lookup, connect, TLS, server
  time, transfer.
- Nothing changes for a check that sets no assertion, except the new
  metrics it reports.

## Non-goals

See the split rows above. Also: assertions on response time percentiles
or across rounds (rules do that), changing what `http.up` means (a
response of any status is up, as today).

## 1. Failure reason on every synthetic plugin

Every synthetic descriptor (`icmp`, `tcp`, `http`, `tls`, `dns`, `snmp`)
gains a discrete metric `<plugin>.failure`, item `""`, reported on every
attempt: `none` when `<plugin>.up` is 1, otherwise one label of the
plugin's vocabulary:

| Label | Meaning | Plugins |
|---|---|---|
| `dns` | the target's name did not resolve | all |
| `refused` | connection refused (TCP RST, ICMP port unreachable for udp) | tcp, http, tls, dns, snmp |
| `timeout` | no answer within the timeout | all |
| `unreachable` | network or host unreachable, no route | all |
| `reset` | the connection was reset or closed early | tcp, http, tls |
| `tls` | the TLS handshake failed | http, tls |
| `certificate` | the certificate did not verify (only with `verify_tls`) | http |
| `protocol` | the response was not valid HTTP | http |
| `redirects` | more redirects than the probe follows | http |
| `nxdomain` | the queried name does not exist | dns |
| `servfail` | the server answered with an error code | dns |
| `auth` | the agent refused the credentials (v3 auth or privacy failure) | snmp |
| `error` | any other failure | all |

- `probe.Down(plugin)` becomes `probe.Down(plugin, reason string)` and
  emits `<plugin>.up = 0` and `<plugin>.failure = reason`; a classifier
  per error source (`net.OpError`, `*net.DNSError`, `context`
  deadline, `x509` errors, `tls.AlertError`/`RecordHeaderError`,
  `http` protocol errors, the dns probe's rcode, gosnmp's errors) maps an
  error to a label. No error text leaves the probe (the client's errors
  quote URLs).
- An attempt that could not be made (bad params, a refused dial by the
  guard, a missing secret) keeps today's shape: an error on every metric,
  `<plugin>.failure` included. The guard's refusal stays an error
  ("address not allowed"), not a failure label.
- A successful attempt reports `none`.
- `tcp`'s banner mismatch, `dns`'s answer mismatch and http status codes
  are not failures (they keep their own metrics; the attempt was up).

## 2. More `http` metrics

All on item `""`:

| Metric | Kind | Value |
|---|---|---|
| `http.dns_ms` | numeric | time resolving names, summed over every hop; absent when no hop resolved a name (an IP literal URL) |
| `http.connect_ms` | numeric | TCP connect time, summed over hops |
| `http.tls_ms` | numeric | TLS handshake time, summed over hops; absent when no hop used TLS |
| `http.ttfb_ms` | numeric | from writing the last hop's request to its first response byte |
| `http.transfer_ms` | numeric | from the first response byte to the end of the body read |
| `http.redirects` | numeric | redirects followed (0 with `follow_redirects` false) |
| `http.content_bytes` | numeric | body bytes received, counted up to 16 MiB (only the first 1 MiB is kept, as today); a larger body makes this metric an error "larger than 16 MiB" and stops the read |
| `http.cert_expiry_days` | numeric | the final hop's leaf certificate, days until `NotAfter` (fractional, negative when expired); absent for http |
| `http.cert_issuer` | discrete | the final hop's leaf issuer: its Common Name, else its first Organization, else the issuer DN string, cleaned with `probe.Label` and cut to 128 bytes; absent for http |

- Measured with `net/http/httptrace` (DNS, connect and TLS hooks fire
  through the guard's `net.Dialer`, which resolves names itself). Hops
  over one connection count once.
- `http.response_ms` keeps its meaning (request start to the end of the
  body read). With a `HEAD` request the body read is empty and
  `transfer_ms` is 0.
- The body is always read to its end, up to 16 MiB, to count
  `content_bytes` (today a body is read only up to 1 MiB); the first
  1 MiB is kept when `body_contains`, `json_values` or a body assertion
  needs it, the rest counted and discarded. The read stays under the
  check's timeout.
- `tls` gains `tls.cert_issuer` (discrete, the same rule as
  `http.cert_issuer`), reported whenever a leaf certificate was received.
- A failed attempt (`http.up` 0) reports `http.failure` and none of the
  new metrics; an attempt that could not be made puts the error on every
  metric the URL is certain to report on an up attempt (`FailAll`):
  `up`, `failure`, `status_code`, `response_ms`, `connect_ms`, `ttfb_ms`,
  `transfer_ms`, `redirects`, `content_bytes` (plus `body_match` and the
  `json_values` items as before); `dns_ms` only when the URL's host is a
  name (not an IP literal, not `{address}`, which `FailAll` cannot
  resolve); `tls_ms` only for an https URL; never `cert_expiry_days` or
  `cert_issuer` (the final hop may be http). (Amended in review, Claude's
  call.) Why: a `check_current` row is never deleted and an error row
  reads unknown, so an error on a metric the URL never reports again would
  keep the host unknown after it recovers; a metric left out only goes
  stale during the outage and is refreshed by the next up attempt.

## 3. Assertions (`http` only)

### Parameter

`assertions`: list, at most **32** entries, each at most 512 bytes,
format `FormatAssertion` (new), entry `name=expression` optionally
followed by ` @warn` (default severity `crit`; ` @crit` is accepted and
canonicalised away). `name` follows the json_values name rule (an item
name); names are unique across `assertions` and `json_values` together.

### Grammar

```
assertion  = subject SP op [SP operand]
subject    = "status" | "response_ms" | "ttfb_ms" | "size" | "redirects"
           | "cert_days" | "cert_issuer" | "body"
           | "header(" string ")" | "json(" string ")" | "html(" string ")"
           | "html(" string ")" SP "count"
op (numeric subjects: status, response_ms, ttfb_ms, size, redirects,
    cert_days, html(..) count)
           = "<" | "<=" | ">" | ">=" | "==" | "!=" | "in"   ; in: a..b, inclusive
op (text subjects: cert_issuer, body, header(..), json(..), html(..))
           = "==" | "!=" | "contains" | "!contains" | "matches" | "!matches"
op (header(..), json(..), html(..) only)
           = "exists" | "!exists"                         ; no operand
operand    = number | number ".." number | string
string     = a JSON string literal ("..." with JSON escapes)
```

- Numbers are decimal (optional sign and fraction). `in` needs `a <= b`.
- `matches` uses Go's RE2 (`regexp`), pattern at most 256 bytes,
  compiled at validation; a pattern that does not compile is refused.
  (Amended in review, Claude's call:) Go's matcher costs up to the
  compiled program's instructions times the input's bytes in one call
  that cannot be interrupted, and a short pattern can compile to a long
  program (`.{1000}`), so a pattern compiling to more than 1 000
  instructions is refused ("the pattern is too complex"), and before
  each match the program size times the input length is charged to a
  per-check budget of 160 000 000 (about one second of CPU); past it the
  assertion is the error "the pattern takes too much work for this
  response". This applies to every `matches`/`!matches` subject.
- `header("X")`: the final response's header, case-insensitive name,
  several values joined with ", ". `Set-Cookie` may be asserted
  (`exists`, `contains` …) but its actual value is never stored (below).
- `json("$.path")`: the JSONPath dialect of `json_values` (same parser,
  same 1 MiB and gate). `json` is a text subject: a string compares by
  its value, a number by its text as `json_values` prints it, a boolean
  as `true`/`false`, `null` as `null`; an object or array never equals
  or contains anything (the assertion fails, actual `object` or
  `array`), though `exists` passes for it.
- `html("selector")`: a CSS selector (the subset below, at most 256 bytes,
  compiled at validation); the text subject is the first match's text
  content, whitespace collapsed; `count` is the number of matches.
  The page is the kept first 1 MiB, built into a tree (below) in a slot
  of the same gate as the JSON stages (`JSONGate`; renamed only if it
  stays readable), so HTML work is bounded across all probes.
  Selector subset (Amended in review, Claude's call; replaces cascadia,
  so the new dependency is dropped): a match by an external library runs
  in one call that cannot be interrupted, and every construct whose cost
  grows with the page (subtree searches, sibling walks, nested
  descendant combinators, attribute regexes) was a way to hold a
  manager-wide gate slot for minutes. Pulsix parses and matches a small
  subset itself: type selectors and `*`; `#id`; `.class`; `[attr]`,
  `[attr="v"]`, `[attr~="v"]`, `[attr^="v"]`, `[attr$="v"]`,
  `[attr*="v"]` (a value is a quoted string or an identifier, compared
  case-sensitively; type and attribute names in lower case);
  compounds of these; the descendant (space) and child (`>`)
  combinators; groups with `,`; `:first-child`, `:last-child` and
  `:nth-child(N)` with N a whole number, `odd` or `even`. Everything
  else (other combinators, pseudo-classes and pseudo-elements, escapes,
  comments, attribute flags) is refused at validation, naming the
  unsupported part. At most 8 compound selectors per comma-separated
  part and 16 simple selectors per compound. The match is one walk of
  the page in document order carrying, per part, the set of compounds
  matched by ancestors (no backtracking: linear in nodes × compounds ×
  simple selectors); every step and every attribute byte read is
  charged before it is taken, to 15 000 000 steps per assertion and
  60 000 000 per check (about a quarter and one second of CPU), past
  which the assertion is the error "the selector took too much work";
  the walk also stops at the probe's deadline. Type and attribute
  names compare without ASCII case (SVG's `linearGradient`, `viewBox`),
  namespaced attributes (`xlink:href`) never match, and a compound names
  at most one `#id`. Tree building (Amended in review, Claude's call;
  replaces round 3's token pre-pass, which was no upper bound on the
  parser's work): the HTML parsing algorithm is not used. Its tree
  builder clones open formatting elements at every insertion (450 `<b>`
  left open over 60 000 paragraphs make 244 KB into 27 M nodes and
  4 GB, in one call that cannot be interrupted), and no estimate beside
  it tracks what it will do. Pulsix builds the tree itself in one pass
  over the tokens of `golang.org/x/net/html`'s tokenizer, so structure
  and tokens never disagree: a start tag is a child element of the
  current one and is opened unless it is void or self-closing; text is
  a text node (raw text — `script`, `style`, `title`, `textarea` … — as
  the tokenizer reads it, wherever it is); an end tag closes the
  nearest open element of its name and everything opened after it, or
  nothing; comments and doctypes are dropped. A small table of implied
  closes covers the common omitted end tags: a block-level start tag
  closes an open `p` (up to the nearest block container), `li` an open
  `li` (up to `ul`/`ol`), `dt`/`dd` an open `dt`/`dd` (up to `dl`), `tr`
  an open row or cell (up to a section or `table`), `td`/`th` an open
  cell (up to `tr`/`table`), `thead`/`tbody`/`tfoot` an open section,
  row or cell (up to `table`), `option` an open `option`, `optgroup` an
  open `option` or `optgroup` (up to `select`/`datalist`), `body` an
  open `head`. No `html`, `head` or `body` is implied, and malformed
  markup can group differently than in a browser. Each token
  makes at most one node with bounded work (open elements are indexed by
  name); at most 512 elements are open (deeper start tags become
  children of the 512th); past 400 000 nodes the build stops and every
  html assertion of the check is the error "the page is too complex to
  parse". The build looks at the probe's deadline at every token and
  runs, with the match, in the gate slot.
- `body`: the kept first 1 MiB as text (invalid UTF-8 kept as bytes).
- `size` is `http.content_bytes`; `cert_days`/`cert_issuer` the https
  metrics; `status`, `response_ms`, `ttfb_ms`, `redirects` the metrics of
  the same names.
- The descriptor hook refuses: a malformed expression (the error names
  the entry), a duplicate name (also against `json_values`), body
  subjects (`body`, `json`, `html`, `size`) with `method` `HEAD`,
  more than 32 entries.

### Result

Per assertion, item = its name:

| Metric | Kind | Value |
|---|---|---|
| `http.assertion` | discrete | `pass`, or the entry's severity `warn` / `crit` when it failed |
| `http.assertion_actual` | discrete | what was seen, cleaned with `probe.Label`, cut to 128 bytes: a number as printed (`503`, `1834` for ms, no unit), a text value quoted as a JSON string, `absent` when the header, JSON path or selector matched nothing; `(hidden)` for `Set-Cookie` |

- Evaluated only when `http.up` is 1. On a failed attempt (`http.up` 0)
  the assertion items report nothing, as `json_values` items do today:
  they turn stale, and the seeded `http.up` rule (below) alerts at once
  with the failure reason. An error on an item would make the host
  unknown at once and open the host-scope unknown alert, which carries no
  reason. An attempt that could not be made (`FailAll`) keeps today's
  shape: the error on every metric of every assertion item.
- An assertion that cannot be evaluated is an error on both metrics, not
  a failure: the body went past 1 MiB for `!contains`, `!matches`,
  `!exists` on `body`/`html`/`json`, or for any `html`/`json` subject;
  JSON or HTML that does not parse; `cert_*` on an http URL; the gate's
  wait timed out; a selector or a pattern that took too much work
  (above). `contains`, `matches`, `exists` on a truncated body
  still pass when found.
- `exists`/`!exists` and `==`/`!=` against an absent value: `exists`
  fails, `!exists` passes, `!=` passes, every other op fails.
- Assertions add no item while `assertions` is empty.

### Rules

Migration 00104 (and `rules.SeedRules()`) seeds seven org rules, each on
the check key of its plugin (matching every check of the plugin since
3.13e):

| Name | Check key | Rule | Severity |
|---|---|---|---|
| Web assertion failed | `http` | `http.assertion in ("warn")` | warn |
| Web assertion failed (critical) | `http` | `http.assertion in ("crit")` | crit |
| Ping check down | `icmp` | `icmp.up < 1` | crit |
| TCP check down | `tcp` | `tcp.up < 1` | crit |
| HTTP check down | `http` | `http.up < 1` | crit |
| TLS check down | `tls` | `tls.up < 1` | crit |
| DNS check down | `dns` | `dns.up < 1` | crit |

Same backfill shape and tests as 00097 (skip an organization that holds a
rule of the same name). Upgrading turns a currently failing synthetic
check into a crit alert on the first evaluation after the migration;
`docs/upgrades.md` says so.

## 4. Alert detail

- Migration 00104 adds `alerts.detail text` and `alert_events.detail
  text` (nullable, additive).
- When the alerts executor opens a tuple alert or changes its severity
  (`ActionOpen`, `ActionSeverity`), it captures a detail for the subject
  through a port (`alerts.DetailSource`, implemented in `internal/app`
  over the checks store and service), inside the same transaction:
  - metric `<plugin>.up` of a synthetic plugin → the current
    `<plugin>.failure` label of the same host, check key and item, when
    it is not `none` (e.g. `timeout`);
  - metric `http.assertion` → `<expression> — saw <actual>`: the entry's
    expression (without the name and severity) from the host's resolved
    check definition of that key, and the current
    `http.assertion_actual` of the same item; either part omitted when
    unavailable;
  - anything else → no detail.
  Cut to 512 bytes. The detail is written to the event row and to the
  alert's `detail` column (the latest). The lookup runs inside a
  savepoint, so a failing lookup rolls back only itself, logs and leaves
  the detail empty; it never fails the transaction. A severity action
  carries only the alert id; the executor takes the subject from the open
  alerts Reconcile was given.
- Close and flap events carry no detail.
- API: the alert views (`List`, `Get`) and each timeline event gain
  `detail` (omitempty). Notifications: `notify.AlertItem` gains `Detail`
  (`json:"detail,omitempty"`), taken from the event the item is about
  (an escalation, which is built from the alert row, takes the alert's
  latest `detail`; its own `escalated` event stores none), rendered after
  the subject in email and chat text (`agentText`-cleaned, since parts
  come from the monitored site). Webhook templates get no new
  placeholder: the field reaches them inside `alerts.items` (the text
  line) and `alerts.items | json`, like every item field; the hostile
  template sample gains a detail.
- SPA: the alert row and the host alerts card show the detail under the
  subject; the timeline shows it per event; failure labels and
  `pass`/`warn`/`crit` are translated (en/de).

## 5. SPA

- Checks dialog, `http`: an assertion editor for `assertions` — one row
  per entry with name, subject (with its argument for
  `header`/`json`/`html`), operator (filtered by subject), operand
  (hidden for `exists`), severity; rows compose the canonical text; a
  "text" toggle edits the raw list; validation errors from the API shown
  per row. The other list params keep their editor.
- Host page, an `http` check: a stacked bar of the latest breakdown (dns,
  connect, tls, ttfb, transfer) with the values; an assertion table
  (name, expression, severity, status badge, actual, age). Failure labels
  show translated next to `<plugin>.up` for every synthetic check.
- The generated schema and fixture regenerate as the "Adding a plugin"
  note in `internal/agent/checks/plugins/registry.go` lists; en/de texts
  for the new params and metrics.

## 6. Vantage points (3.13a)

The agent runs the same `internal/probe` code, so assertions, the
breakdown and the failure reason are probed from agents too. The combined
result takes the representative vantage's whole round (3.13a's rule),
assertion items included. `probeFit` admits the new metrics through the
descriptor and the assertion names through `ItemAsked`. No proto change.
An agent of 3.13a (unreleased, so only a development concern) would not
report the new metrics; `docs/upgrades.md` notes the skew.

## Tests

- Failure classifier: table per plugin (refused, timeout, unreachable,
  dns, reset, tls, certificate, protocol, redirects, nxdomain, servfail,
  auth) with fakes or loopback servers; `none` on success; guard refusal
  stays an error.
- Breakdown: httptest server with a delay; https with a test CA; a
  redirect chain summing; IP literal without `dns_ms`; HEAD; 16 MiB cap.
- Assertion parser: every subject × op, operands, `in` order, `@warn`,
  JSON string escapes, refusals (bad regex, bad selector, HEAD with body
  subjects, duplicate names across lists, > 32, > 512 bytes).
- Assertion evaluation: pass/fail per op, absent values, truncation
  rules, `Set-Cookie` hidden, errors when down, html and json via the
  gate, actual formatting and 128-byte cut.
- Seed migration as 00097's tests; alert detail capture for `up`,
  assertion and other metrics, failing lookup tolerated; notification
  rendering with detail (email, chat, webhook placeholder); API fields;
  SPA editor, host-page bar and table, alert detail rendering.
- Descriptor goldens, catalogue, SPA fixture as the "Adding a plugin"
  note lists.

## Documentation

`docs/BACKLOG.md` (3.13b done, 3.13f noted as delivered by it; new rows
for the split-out items), `docs/upgrades.md` (migration 00104; skew
note; no new dependency since review); noix-docs on a new branch
`docs/pulsix-web-assertions` from `docs/pulsix-probe-vantages`: check
plugins reference (failure reasons, new http/tls metrics, assertions
with grammar and examples), alerts/notifications pages (the detail, the
new placeholder), changelog. No tech stack named.
