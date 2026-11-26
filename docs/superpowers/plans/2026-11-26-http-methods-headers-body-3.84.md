# Request methods, headers and a body on the http check (backlog 3.84) — implementation plan

> **For agentic workers:** steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 3.84: "Other request methods and custom request headers or bodies — From 3.13b. The `http` check sends GET or HEAD only, with no request headers or body of its own (credentials come from a secret). Add `POST` and friends, headers and a body."

**Architecture:** a leaf package `internal/probe/httpreq` holds the request rules both sides use (methods, which take a body, header entry parsing, the media type shape, the bounds). `internal/checkplugins` validates the three new `http` parameters with it (two new formats with SPA twins, cross-parameter rules in `httpCheck`) and gains a descriptor hook `ManagerOnly` that keeps such a check off agent vantages. `internal/probe/http` builds the request from the same parser. `internal/probes` (runner and view) read "probed only from the manager" through one descriptor method instead of the secret test alone. The SPA renders the body as a textarea, warns on state-changing methods and closes the vantage group picker for a custom request, as it does for a secret.

**Spec:** the backlog row above; no separate design doc.

## Global Constraints
- Layers per `CLAUDE.md`; package tests only (`go test -race -timeout 900s ./internal/<pkg>/...`, `internal/api` with `-timeout 1800s -run …`); never `go test ./...`, `make test` or `internal/e2e`.
- `golangci-lint run` on touched packages and `gofmt -s -l` clean before each commit; SPA `npx vitest run <files>`, `npm run lint`, `npx tsc -b`.
- Generated files only through their generators: `web/src/test/checkPlugins.json` (`go test ./internal/checkplugins -run SPAFixture -update-spa-fixture`), `web/src/api/schema.d.ts` (`npm run api:gen`).
- No migration, no proto change. Tests never dial anything but httptest servers.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **R1 Methods:** `method` takes GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS; default GET as before. — The set the backlog row and common monitors offer; CONNECT and TRACE are proxy/debug methods with no monitoring use. — Cost if wrong: one more enum option.
- **R2 No confirmation flag for state-changing methods.** POST, PUT, PATCH and DELETE need no extra parameter; the forms show a warning under the method ("sent every interval; the endpoint must not change anything each time") while one is chosen. — Choosing the method is the explicit act; a flag in the stored config adds no safety for API callers (they set both) and would force a value onto every later client. — Cost if wrong: an additive boolean parameter that defaults to true for existing checks.
- **R3 A body only with POST, PUT or PATCH.** `body` (string, not empty, at most 8 KiB, text as given, no format) with any other method is refused (`body needs method POST, PUT or PATCH`). — GET/HEAD/DELETE/OPTIONS bodies have no defined meaning (RFC 9110) and some servers reject them. — Cost if wrong: widen the set in `httpreq.TakesBody`.
- **R4 Content type:** `content_type` (string, at most 255 bytes, format `mediaType`: `type/subtype` tokens with optional `; name=value` parameters) is sent as `Content-Type` with the body; absent means `application/json`. It is refused without a body. `Content-Type` in `headers` is refused (use `content_type`). — One place for it, and a default that fits the usual JSON API. — Cost if wrong: a check posting form data must name its type.
- **R5 Headers:** `headers` is a list of at most 16 entries of at most 1 024 bytes, each `Name: value` (format `httpHeader`): the name an RFC 9110 token, the value without control characters other than tab (so no CR, LF or NUL), surrounding spaces and tabs trimmed, empty allowed. A name twice (case ignored) is refused by `httpCheck`. — Bounded, injection-free, the same rules net/http's own validation applies.
- **R6 Reserved and credential headers are refused in `headers`.** The probe controls `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `User-Agent` (the secret header's existing list), the hop-by-hop `Keep-Alive`, `Proxy-Connection`, `TE`, `Trailer`, `Upgrade`, and `Expect`, `Accept-Encoding` (the transport's transparent decompression), `Content-Type` (R4). `Authorization`, `Proxy-Authorization` and `Cookie` are refused with "carries credentials: use the check's secret" — the existing secret (`header` and `value` fields) sends any one such header and never appears in a definition, a response or a log. — Credentials never in plain config; one credential mechanism. — Cost if wrong: someone wanting `Host` for a virtual host by address (needs SNI handling, its own item) or a custom `User-Agent`.
- **R7 The secret wins:** the secret's header is applied after the plain headers and replaces a plain header of the same name; on a redirect that leaves the origin it is removed as before (a plain header of that name with it). Plain headers follow redirects as net/http carries them; a 301/302/303 after a POST turns into GET without a body, a 307/308 resends the body (browser behaviour, net/http's default).
- **R8 Version skew — a custom request is probed only from the manager.** An agent of an earlier release runs the probe's old code: a method other than GET/HEAD fails there as "invalid method" (unknown, not wrong), but headers and a body would be silently dropped and a plain GET reported. No capability bit exists for probe features (only `probes_supported`), and adding one needs a proto field, a column and per-check eligibility. So, like a check with a secret: `vantage_groups` is refused when the method is not GET or HEAD or `headers`, `body` or `content_type` is set ("checks with a custom request are probed only from the Pulsix server"), and the runner and the host-page view ignore agent vantages of such a check (descriptor hook `ManagerOnly`, read with the secret through `Descriptor.ProbedOnlyFromManager`). — Safe with every agent version, no protocol change. — Cost if wrong: agent vantages for custom requests become a follow-up (a heartbeat capability list, an additive column, and per-check eligibility in `planVantages`).
- **R9 Several managers:** an older manager still running during a rolling upgrade ignores `headers`, `body` and `content_type` in a stored check and, if it holds the probe lock, sends a plain GET. Documented in `docs/upgrades.md`: set these up only once every manager runs this version (the 2.121 precedent). A new method fails as "invalid method" there.
- **R10 Nothing echoed:** the body, header values and content type never appear in observations, errors or logs; a header or body the probe refuses (only reachable past validation) fails the attempt as "invalid request header" / "invalid request body" without quoting it.
- **R11 HEAD rules generalised:** `json_values` and body-reading assertions are refused only with HEAD, as before, with messages reworded from "needs method GET" to "needs a method other than HEAD". OPTIONS and DELETE responses are read like GET's.

### Fix round 1 (independent review `3.84-review.md`, controller rulings; the owner reviews R12 on 2026-11-27)
- **R12 Writes need the operator's switch (review I1).** POST, PUT, PATCH, DELETE and any body need the manager setting `PULSIX_PROBE_WRITE_METHODS` (boolean, default `false`, read like the other PULSIX_ booleans). Off: `checks.RefuseWrites` refuses such a definition on create and update (organization and shared definitions, 400 naming the setting), and the probe refuses to send one through a guard without writes (`probe.Guard.WithWrites`, carried by the guard every probe already receives), so a check stored while it was on reads "state-changing requests are turned off on this server (PULSIX_PROBE_WRITE_METHODS)" on every metric, never a plain GET. An agent's guard never allows writes. GET, HEAD and OPTIONS with headers need no switch. Supersedes R2's "no flag" for the server side; the form keeps its warning. — Without it any `checks.write` member could send writes and bodies into every network the manager reaches outside `PULSIX_PROBE_DENY_CIDRS`. — Cost if wrong: an operator who wants POST health checks sets one variable.
- **R13 Routing and override headers are always refused (review I1):** `X-Forwarded` and every `X-Forwarded-*`, `Forwarded`, `X-Real-IP`, `X-Original-URL`, `X-Rewrite-URL`, `X-HTTP-Method-Override`, plus the common variants `X-HTTP-Method` and `X-Method-Override`. — A target or its proxy may trust them for the client's address, the path or the method.
- **R14 Plain headers stay on the first origin (review M1, amends R7):** once a redirect leaves the first request's origin (or goes https to http), the plain headers are removed along with the secret's, and stay removed.
- **R15 Credential-looking header names are refused (review M1):** the secret's header-and-value form already sends any one header name, so names containing `token`, `secret`, `apikey`, `api-key` or `password` (case ignored; `X-Api-Key`, `X-Auth-Token`, `Private-Token`, …) are refused like Authorization, with the pointer to the secret. — Cost if wrong: a harmless header such as `X-Csrf-Token` must go through a secret.
- **R16 No advertised default for `content_type` (review M2):** the descriptor shows `application/json` as an example; the probe still applies it to a body sent without a type.
- **R17 Body text only (review M3):** `body` must be UTF-8 without NUL (PostgreSQL jsonb cannot store NUL; it was a 500), header values UTF-8; validated (400) and re-checked by the probe. `body_contains` and other string parameters keep the pre-existing NUL gap.
- **R18 Slow-body breakdown test (review M5):** asserts what is deterministic (`response_ms >= 200`, `ttfb_ms < 200`, `transfer_ms >= 100` with slack for the read goroutine's scheduling) instead of injecting a clock, which would need a seam through `NewProbe` for one test.

## Review Focus
- A header entry with CR/LF, a non-token name or a reserved/credential name passes nowhere: `TestHeaderProblem` (httpreq), `formats.json` vectors (Go and TS), `TestHTTPParams`.
- A body or content type sent with the wrong method, or headers twice: `TestHTTPCheckRequestRules`.
- An agent never receives a custom request: `TestHTTPCustomRequestRefusesVantageGroups`, `TestCustomRequestCheckStaysManagerOnly` (runner), `TestHostVantagesExpectedOnly` extended (view).
- The probe sends exactly what was configured, the secret wins over a plain header, and the body survives a 307 but not a 303: `TestProbeSendsMethodHeadersAndBody`, `TestSecretHeaderReplacesPlainHeader`, `TestRedirectBodyFollowsBrowserRules`.
- No configured value leaks: `TestRequestValuesNeverInObservations`.

## Task 1: Request rules and validation (`internal/probe/httpreq`, `internal/checkplugins`)
- Files: `internal/probe/httpreq/httpreq.go` (+ test); `internal/checkplugins/http.go`, `formats.go`, `vantage.go`, `descriptor.go`, `testdata/formats.json`; `internal/api/check_plugin_schema.go` (format phrases) and its test; SPA fixture regenerated; `web/src/features/checks/paramFormats.ts` twins (+ vectors run by `paramFormats.test.ts`).
- Interfaces: `httpreq.Methods`, `TakesBody(method) bool`, `HeaderProblem(entry) string`, `ParseHeader(entry) (name, value string, ok bool)`, `MediaTypeProblem(s) string`, bounds `MaxHeaders`, `MaxHeaderLen`, `MaxBodyLen`, `MaxContentTypeLen`, `DefaultContentType`; `checkplugins.FormatHTTPHeader`, `FormatMediaType`; `Descriptor.ManagerOnly func(map[string]any) bool`, `Descriptor.ProbedOnlyFromManager(params) bool`.
- Tests: `TestHeaderProblem`, `TestParseHeader`, `TestMediaTypeProblem`, `TestTakesBody`; `TestHTTPParams` cases, `TestHTTPCheckRequestRules`, `TestHTTPCustomRequestRefusesVantageGroups`, `TestProbedOnlyFromManager`.
- [ ] Commit `feat(checks): http checks take other methods, headers and a body`.

## Task 2: The probe sends them (`internal/probe/http`)
- Files: `http.go` (request build), a new `request.go` for the headers/body/content type, tests.
- Tests: `TestProbeSendsMethodHeadersAndBody`, `TestSecretHeaderReplacesPlainHeader`, `TestRedirectBodyFollowsBrowserRules`, `TestRequestValuesNeverInObservations`, `TestProbeRefusesUnvalidatedRequest`.
- [ ] Commit `feat(probe): send the http check's method, headers and body`.

## Task 3: Manager-only on the runner and the view (`internal/probes`)
- Files: `secrets.go`/`targets.go`/`vantages.go`/`view.go`: the secret test becomes `ProbedOnlyFromManager`.
- Tests: `TestCustomRequestCheckStaysManagerOnly`, view test extended.
- [ ] Commit `feat(probes): probe custom http requests only from the manager`.

## Task 4: SPA
- Files: `ParamFields.tsx` (body textarea, method warning), `VantageFields.tsx` (picker closed for a custom request), `paramForm.ts` (`isMultiline`, `customRequest`), i18n en/de (labels, hints, format messages), `schema.d.ts` regenerated.
- Tests: `CheckDialog.http.test.tsx` (method warning, body textarea, headers sent), `CheckDialog.vantage.test.tsx` (picker closed with headers), `paramForm.test.ts`.
- [ ] Commit `feat(web): edit an http check's method, headers and body`.

## Task 5: Docs
- Backlog 3.84 → done; `docs/upgrades.md` (R8, R9).
- [ ] Commit `docs: backlog 3.84 done`.
