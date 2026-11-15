# HTTP JSON values (backlog 3.6h, HTTP half) — design

Status: the owner accepted the proposal on 2026-11-15 ("passt weiter"): the
`http` check gains `json_values` (`name=JSONPath`, at most 32, RFC 9535,
the library already used by the triggers); the body (up to the existing
1 MiB cap, any content type) is parsed as JSON; a path must select exactly
one scalar — a number becomes `http.json.value`, a string or boolean
`http.json.text` (booleans as `true`/`false`), item = the entry's name;
anything else is an error on that item only; invalid or oversized JSON is
an error on every JSON item while `http.up` and the status code stand;
`json_values` needs method GET; no new seed rules. Optional `secret` from
the organization's secret store: exactly one of `username`+`password`
(Basic), `token` (Bearer) or `header`+`value` (custom header); credentials
never follow a redirect to another host; values never in error texts. Not
part of it: POST and request bodies, assertions inside the check (3.13b),
bodies over 1 MiB. The SNMP half of 3.6h was delivered by 3.13d. Every
other decision below is **(Claude's call)**. Amended after the Task 2
review (controller's ruling): paths are a bounded RFC 9535 subset with
Pulsix's own parser and evaluator instead of the library (see Descriptor).

## Descriptor (`internal/checkplugins/http.go`)

- `json_values`: list, at most 32, each `FormatJSONValue`: `name=path`,
  name the secret-name pattern `[A-Za-z0-9_.-]{1,64}`, path at most 512
  bytes and accepted by `internal/jpath`, Pulsix's own parser for a
  bounded subset of RFC 9535 (same semantics as the RFC for what it
  accepts); names unique (Check hook). The subset: `$`, then `.name`,
  `.*`, `['name']`, `[index]`, `[*]`, `[?filter]`, one selector per
  bracket (no unions, no slices), at most one descendant segment (`..name`,
  `..*`, `..[selector]`); a filter is comparisons `@path op literal` and
  existence tests `@path` (an `@path` of names and indexes only) joined by
  `&&`, `||`, `!` and parentheses, at most 8; no `$` inside a filter, no
  functions, no nested filters. Why (review of Task 2): the target writes
  the document the path runs over, and full RFC 9535 cannot be bounded —
  unions and descendant segments multiply the node list, `$` queries
  inside filters make evaluation polynomial in the body, and
  `match`/`search` compile patterns the document can supply — whereas
  every path of the subset evaluates in time linear in the body.
  `internal/triggers` keeps the full library for its own, operator-written
  documents.
- `secret`: `SecretParam()`, optional.
- Check hook: `json_values` non-empty requires `method` `GET`.
- Metrics added: `http.json.value` (numeric, item = name),
  `http.json.text` (discrete, item = name).
- Items: `ItemAsked` (the hook 3.13d added) — an item is asked when it is
  a `json_values` name. Whole-host metrics stay item `""`.
- The SPA twin of `FormatJSONValue` checks the `name=` shape, the name
  pattern, a path starting with `$`, the length and at most one `..`; the
  server is the authority on the subset.

## Probe (`internal/probe/http`)

- The body is read once, up to `httpBodyLimit` (1 MiB). `body_contains`
  and `json_values` both use the same bytes. A body longer than the cap,
  or not valid JSON (`encoding/json` into `any`, numbers as `json.Number`
  so large integers keep their precision until converted to float64) →
  every JSON item an error "the response is not JSON" or "the response is
  larger than 1 MiB".
- The probe bounds the evaluation cost on a body the target controls: a
  document nested deeper than 64 arrays/objects → every JSON item an
  error "the response is nested too deeply"; a path `internal/jpath`
  refuses → that item's error "the path is not allowed" (defence in depth
  behind the format); an evaluation that would visit more than 2 000 000
  nodes (a 1 MiB body holds about 525 000 values) → that item's error
  "the path selected too much", the backstop that keeps every entry's
  work bounded; a body whose read broke off (timeout, reset) → every JSON
  item "the response was cut short".
- Each path: zero nodes → "the path matched nothing"; more than one →
  "the path matched <n> values"; one node that is an object, array or
  null → "the path matched an object" / "a list" / "null"; a number →
  `http.json.value` (a number outside float64's finite range → item error
  "the number is out of range"); a string → `http.json.text`, cleaned like
  the other labels (valid UTF-8, no control characters, 512-byte bound);
  a boolean → `http.json.text` `true`/`false`.
- A status code is reported as today whatever it is; JSON values are
  extracted from any status's body (an API's error JSON can be useful).
- **Credentials:** the runner hands the secret (3.13d's `secret`
  argument). Exactly one form must be present (`username`+`password`,
  `token`, `header`+`value`); anything else → every metric an error
  naming the secret and the problem, never a value; a `header` name must
  be a valid HTTP token and not one the probe controls (`Host`,
  `Content-Length`, `Transfer-Encoding`, `Connection`, `User-Agent`). The header is set
  on the first request; on a redirect it is kept only when scheme, host
  and port are unchanged (Basic and Bearer the same way, implemented in
  the redirect policy), and an https → http downgrade drops it too.
- Errors from the HTTP client are never passed through with a URL that
  could carry userinfo; a `url` with userinfo is refused by the format
  already (check; refuse if not).

## Runner (`internal/probes`)

`http` now uses a secret when its `secret` param is set. A missing or
unreadable secret must fail only the metrics the check would produce
(`http.up`, `http.status_code`, `http.response_ms`, `http.body_match` when
`body_contains` is set, and the JSON items by name) — a `FailAll` in the
http package like `snmp.FailAll`, so no item-less phantom rows appear.

## SPA

Fixture, en/de texts for the new params and metrics, the `FormatJSONValue`
twin, the `secret` hint for a server plugin (already worded for
Settings → Secrets).

## Tests

Descriptor and format (valid paths, invalid syntax, length, duplicate
names, HEAD with json_values); probe against an in-test HTTP server: each
scalar type, each item error, large integers, invalid JSON, a body over
the cap, both features together, non-2xx with JSON; credentials: each
form, invalid combinations, forbidden header names, redirect to the same
origin keeps the header, to another host or to http drops it, no value in
any error; runner: missing secret fails only the check's own metrics and
items.

## Documentation

`docs/BACKLOG.md` (3.6h done), noix-docs (http check: JSON values with
examples, credentials, redirect rule; organization secrets page lists the
http forms; changelog). No tech stack named.

## Non-goals

POST and request bodies; assertions inside the check; bodies over 1 MiB;
XML or other formats; client certificates.
