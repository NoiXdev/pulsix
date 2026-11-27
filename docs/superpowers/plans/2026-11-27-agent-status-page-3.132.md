# Agent status page (backlog 3.132) — implementation plan

> **For agentic workers:** steps use checkbox (`- [ ]`) syntax.

**Goal:** the agent serves a read-only local status page (HTML and `/status.json`) that shows what it collects and sends, exactly as sent, turned on only by the host's own `agent.yaml`; secret values are replaced by `[secret]` before anything leaves the agent; the agent card shows whether the page is on and where.

**Architecture:** `internal/agent/secrets` parses a new `status_page:` section with a per-section problem (never refusing the rest of the file) and builds a `Redactor` from the loaded secret values, which the watcher publishes lock-free. The loop redacts check results, probe results and facts at the sink (before buffer or send), records what it sent in a `statusRecorder`, and owns a `statuspage.Controller` it re-applies on every look at the file. `internal/agent/logring` is a redacting 100-line ring the binary tees its logger into. `internal/agent/statuspage` is the HTTP server (html/template + JSON, de/en, token via Bearer or login-form cookie, rate-limited failures, security headers, timeouts, connection limit, HTTPS with a given or a self-signed certificate). The heartbeat gains `status_page` (enum) and the heartbeat ack gains the organization's and host's names; the manager stores the mode in `host_agents.status_page` (migration 00138) and the card shows it.

**Spec:** `docs/superpowers/specs/2026-11-27-agent-status-page-design.md` (binding; owner decisions 1–4 and the "My call" sections) and backlog row 3.132.

## Global Constraints
- Layers per `CLAUDE.md`; package tests only (`go test -race -timeout 900s ./internal/<pkg>/...`, `internal/api` with `-timeout 1800s -run …`); never `go test ./...`, `make test` or `internal/e2e`.
- `golangci-lint run` on touched packages and `gofmt -s -l` clean before each commit; SPA `npx vitest run <files>`, `npm run lint`, `npx tsc -b`.
- Generated code only through `make buf-generate`, `make sqlc`, `npm run api:gen` (tools built into the scratchpad, never installed system-wide).
- Migration additive; proto fields additive (older agent ↔ newer manager and back).
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **R1 `status_page` problems are per section.** A wrong value, unknown key, duplicate key, anchor or tag inside the section turns only the page off with a `line N, column M: <fixed reason>` problem (never quoting a value), logged once per change; secrets, commands, checks and probes stay in effect. A YAML syntax error or a second `status_page:` key stays a file-level refusal (the section cannot be isolated from a file that does not parse). — The spec's "My call". — Cost if wrong: a duplicate key refuses the file, as for every other section.
- **R2 Listen rules.** `host:port`, port 1–65535. Loopback = `localhost`, `127.0.0.0/8`, `::1`; `localhost` binds `127.0.0.1` explicitly (a hosts file cannot move it). Anything else (an empty host, a name, another address) is network: a token of 32–1024 characters is required. `tls_cert`/`tls_key` both or neither; given, they are served on any address (loopback included); network without them uses a self-signed P-256 certificate kept as `status-page.crt`/`status-page.key` (0600) in the data dir, regenerated when missing, unreadable or within 30 days of expiry. — Cost if wrong: an operator adjusts the file.
- **R3 The page follows the file within about a minute.** The controller is applied on every look (start, heartbeat, the 30 s periodic look); a changed section closes the old listener and opens the new one; an unchanged one is left alone; a failed bind is retried on the next look and logged once per change of the outcome. — No restart needed; documented.
- **R4 Heartbeat mode.** `StatusPageMode` UNSPECIFIED (older agent, stored NULL, card shows nothing), OFF, LOOPBACK, NETWORK, FAILED (the section is refused or the page could not start; the card says the agent log has the reason). FAILED goes beyond the spec's three words because "shown as an agent.yaml problem" needs a channel the card already reads. — Cost if wrong: one enum value and one string.
- **R5 Names ride every heartbeat ack** as `ManagerMessage.agent_names {org_name, host_name}`, read by the heartbeat statement itself (`RecordAgentHeartbeat` becomes `:one` through a CTE), so no extra query. A heartbeat for an unknown serial returns no names and no error, as before. Older managers send nothing: the page shows the organization ID (certificate SAN) and host ID.
- **R6 Redaction (owner decision 4).** Values of the last successfully read file's secrets, ≥ 4 bytes, longest first, become `[secret]` in check results (item, label, error), probe results (item, label, error) and fact values, at the sink, before buffering or sending; in log lines before they enter the ring. Check keys and metric names are not touched (they are definitions, and rewriting them would move series). A refused file keeps the previous values (fail closed); an absent file clears them. Local check `params_json` in the heartbeat is not redacted (not in the owner's list, and a rewritten param would fail the manager's validation); the page redacts every string it renders as a last pass. — Cost if wrong: a common field value such as `localhost` or `5432` is replaced in results and facts too (accepted by the owner's 4-character rule; documented).
- **R7 "Last sent" means last produced for sending.** The page records each check's and probe slot's last batch after redaction at the sink, whether it went out live or into the buffer; the buffer section says how much is waiting and since when.
- **R8 Auth.** Token compared in constant time on SHA-256 digests; `Authorization: Bearer` or a login form (`POST /login`) that sets `pulsix_status` = `<expiry>.<HMAC-SHA256(token, expiry)>` (HttpOnly, Secure on HTTPS, SameSite=Strict, 12 h); a token change ends every session. Five failures per remote IP per minute, then 429 until the minute is over (at most 4096 tracked addresses). A token set on loopback is enforced (spec).
- **R9 Server limits.** ReadHeaderTimeout 5 s, ReadTimeout 10 s, WriteTimeout 10 s, IdleTimeout 60 s, MaxHeaderBytes 16 KiB, 16 concurrent connections (`netutil.LimitListener`), login body ≤ 4 KiB. GET/HEAD everywhere, POST only on `/login`, else 405.
- **R10 Page.** Server-rendered `html/template`, inline stylesheet with a per-response nonce, `<meta http-equiv="refresh" content="10">` (no script); CSP `default-src 'none'; style-src 'nonce-…'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`, `nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`. Language from `Accept-Language` (highest-q of de/en, en fallback).
- **R11 The repo CHANGELOG is release-please output** and is not edited by hand; the "Unreleased" entry the spec asks for belongs to the user docs (report notes).
- **R12 Version skew.** An agent older than this release refuses the whole `agent.yaml` with a `status_page:` key (unknown top-level key) — update the agent before adding it (as for `probes:`). Older managers ignore the new heartbeat field and send no names; older agents ignore the names and send no mode (card shows nothing). Redaction changes what the manager stores for affected results and facts (upgrades.md).

### Fix round 1 (review `3.132-review.md`, owner decision and controller rulings, 2026-11-27)
- **R6 amended (owner decision): only secret fields are redacted.** The allowlist of non-secret connection fields is the owner's `host`, `port`, `user`, `username`, `database`, `socket`, `tls_server_name`, `tls_ca`, `tls_cert`, `tls_key`; every other field, an unknown one included, is secret. One list: `secrets.nonSecretFields` / `IsSecretField`.
- **R13 Added non-secret fields** (from the plugins' field specs, `postgres`, `mysql`, `redis`, `webcheck`): `tls` (mode: false/preferred/true/skip-verify — "true"/"false" would mangle every line), `pipe` (mysql named pipe name), `sslmode` (postgres mode), `sslrootcert`, `sslcert`, `sslkey` (postgres's file paths, the twins of `tls_ca`/`tls_cert`/`tls_key`), `basic_auth_user` (a user name, like `user`). `password` and `basic_auth_password` stay secret. — Cost if wrong: one entry in the list.
- **R14 Quoted form and cut tails.** Each value is also replaced in its `strconv.Quote` form without the outer quotes (slog's text handler; finding 1). A text cut at its bound (an error at 2048, a fact at 512) also loses a trailing fragment of 4+ bytes that a value starts with (`RedactTail`, finding 8). After redaction a result whose item or label exceeds 512 bytes is dropped with a warning at most once a minute; errors and facts are cut back to their bounds (finding 3).
- **R15 The page starts off the caller's path** (finding 4): attempts run on their own goroutine, `net.ListenConfig.Listen` with a 1 s context; a failed start is retried at most once a minute; a config change starts at once. `Run` waits for the first attempt once (`AwaitStart`, ≤ ~1 s) so the first heartbeat reports what is served; the heartbeat path never waits. Every apply checks a self-signed certificate's end and restarts the page with a new one 30 days before it (finding 7). — Cost if wrong: the card reads off for one heartbeat after an edit.
- **R16 Listen first** (finding 9): a mapping without `listen` is off without a problem, whatever else it holds; a section that is not a mapping stays a problem (it cannot be read as off, and `status_page: 127.0.0.1:8742` clearly asks for the page). `[::ffff:127.0.0.1]` binds 127.0.0.1; a zoned `[::1%lo0]` counts as network (token and HTTPS), erring on the safe side; `[::]` is network.
- **R4 amended (controller ruling, to be shown to the owner): FAILED stays.** A refused `agent.yaml` reports FAILED when the last applied config asked for the page (a listen address or a refused section), OFF otherwise (`Controller.Asked`).
- **R17 Page hardening.** Without a token the page answers only Hosts `localhost`, 127/8 literals and `[::1]` (port optional), 421 otherwise (DNS rebinding, finding 2); with a token any Host. A `/login` POST with `Sec-Fetch-Site: cross-site` or an `Origin` that is not this Host (`null` included) is refused with 403 before it counts as a failure (finding 5). IPv6 failures count per /64; while the 4096-entry table is full of live entries, the shared overflow bucket can block every address not in it for the rest of the minute — accepted and documented: only the page is affected, never monitoring (finding 6). `/login` on an open page answers 405 to methods other than GET, HEAD, POST.
- **R18 The page shows what was sent** (controller ruling): no redaction pass over keys, metric names or anything else the agent sent already redacted; the page redacts only the local checks' `params_json`, which the heartbeat sends as written, so this one field can differ from what the manager receives (documented in upgrades.md).
- Not done: the free-port listen-and-close in the loop tests (controller: skip).

## Review Focus
- No secret on the page, in the JSON or the log ring: `TestRedactorReplacesLongestFirst`, `TestRingRedactsBeforeStoring`, `TestHandlerRedactsEverythingRendered`, `TestLoopRedactsCheckResultsBeforeSending`, `…ProbeResults…`, `…Facts…`, `TestWatcherKeepsRedactorWhileRefused`.
- No network page without a token, no token in clear text: `TestParseStatusPageRules` (network without token refused, short token refused), `TestControllerServesNetworkOverTLSOnly`, `TestAuthRequiredWithToken`, `TestLoginCookieAttributes`.
- A status_page mistake never drops the rest of the file: `TestParseStatusPageProblemKeepsTheFile`.
- Off by default, manager cannot turn it on: `TestLoopStatusPageOffWithoutSection` (heartbeat reports OFF, nothing listens).
- Abuse: `TestFailedTokensAreRateLimited`, `TestMethodsOtherThanGetAre405`, `TestSecurityHeaders`, `TestPageEscapesUntrustedText`.
- Compatibility: `TestHeartbeatStoresStatusPage` (NULL for UNSPECIFIED), `TestHeartbeatAckCarriesNames`, `TestLoopRecordsNamesFromAck`.

## Task 1: `status_page` section and the redactor (`internal/agent/secrets`)
- `statuspage.go`: `StatusPage{Listen, Token, TLSCert, TLSKey string; Network bool}`, `parseStatusPage(n) (StatusPage, problem string)`; `parse.go`: key in `topLevelKeys`, node checks skip the section and run inside it; `Set.StatusPage() (StatusPage, string)`.
- `redact.go`: `Redactor`, `(*Redactor).Redact(string) string` (nil-safe), built in `parse`; `Watcher.Redactor()` (atomic, R6).
- Tests: `TestParseStatusPage`, `TestParseStatusPageRules`, `TestParseStatusPageProblemKeepsTheFile`, `TestRedactorReplacesLongestFirst`, `TestRedactorIgnoresShortValues`, `TestWatcherKeepsRedactorWhileRefused`, `TestSetStringNeverShowsToken`.
- [ ] Commit `feat(agent): status_page section and secret redactor in agent.yaml`.

## Task 2: Redact before sending (`internal/agent`)
- `redact.go`: `redactResults`, `redactProbeResults`, `redactFacts` (copies, never the caller's slice); called in `handleResults`, `handleProbeResults`, `pushFacts`.
- Tests: `TestLoopRedactsCheckResultsBeforeSending`, `TestLoopRedactsProbeResultsBeforeSending`, `TestLoopRedactsFactsBeforeSending`, `TestLoopRedactsBufferedResults`.
- [ ] Commit `feat(agent): replace secret values in results and facts before they are sent`.

## Task 3: Log ring (`internal/agent/logring`, `cmd/agent`)
- `Ring` (100 lines, 4 KiB per line, partial lines held), `SetRedact(func(string) string)`, `Lines()`; `cmd/agent` tees the logger into it and passes `agent.WithLogRing`.
- Tests: `TestRingKeepsLastLines`, `TestRingRedactsBeforeStoring`, `TestRingJoinsPartialWrites`, `TestRingCapsLineLength`; `TestRunAgentTeesLogIntoRing` (cmd/agent).
- [ ] Commit `feat(agent): keep the last log lines in memory, secrets replaced`.

## Task 4: Protocol and manager (`proto`, `internal/agents`, `internal/agentapi`, `internal/api`, migration 00138)
- `Heartbeat.status_page = 9` (`StatusPageMode`), `ManagerMessage.agent_names = 4` (`AgentNames`).
- Migration `00138_agent_status_page.sql`: `host_agents.status_page text NULL CHECK (… IN ('off','loopback','network','failed'))`; `RecordAgentHeartbeat` writes it and returns names; `UpsertHostAgent` resets it like the reports.
- `agents.HeartbeatReport.StatusPage`, `Service.Heartbeat` returns `Names`; `Agent.StatusPage *string`; `agentapi` puts names on the heartbeat ack; `agentDTO.status_page` (nullable enum).
- Tests: `TestHeartbeatStoresStatusPage`, `TestHeartbeatReturnsNames` (agents, testdb), `TestHeartbeatAckCarriesNames` (agentapi), `TestAgentDTOReportsStatusPage` (api); `internal/backup` and `internal/db` suites run.
- [ ] Commit `feat(agents): store the agent's status page mode and name the agent's host on the ack`.

## Task 5: Status page server (`internal/agent/statuspage`)
- `model.go` (Snapshot), `handler.go` (routes, headers, methods, html/json, language), `auth.go` (token, cookie, limiter), `page.go` (template, de/en texts), `server.go` (`Controller.Apply(Config)`, `Mode()`, `Close()`, listener, limits, TLS), `selfsigned.go`.
- Tests per Review Focus plus `TestPageShowsEverySection`, `TestJSONMatchesSnapshot`, `TestLanguageFromAcceptLanguage`, `TestSelfSignedIsReusedAndRenewed`, `TestControllerFollowsConfigChange`.
- [ ] Commit `feat(agent): local read-only status page server`.

## Task 6: Agent wiring (`internal/agent`, `checks`, `probes`)
- `Scheduler.Checks()`, `Runner.Assigned()`, `Ring.Stats()`; `status.go` (`statusRecorder`, `Loop.statusSnapshot`), controller applied in `lookAndApplyLocal`, closed when `Run` returns; heartbeat `status_page`; ack names recorded; contact, heartbeat, facts, connection times recorded.
- Tests: `TestLoopStatusPageOffWithoutSection`, `TestLoopServesStatusPageOnLoopback`, `TestLoopRecordsNamesFromAck`, `TestStatusSnapshotShowsWhatWasSent`, `TestSchedulerChecks`, `TestRunnerAssigned`, `TestRingStats`.
- [ ] Commit `feat(agent): serve the status page from agent.yaml and report it in the heartbeat`.

## Task 7: Agent card (SPA)
- `schema.d.ts` regenerated; `HostAgentCard` row "Status page" (off / on, local only / on, network / off, see log); i18n en/de.
- Tests: `HostAgentCard.test.tsx` ("status page").
- [ ] Commit `feat(web): the agent card shows whether the agent's status page is on`.

## Task 8: Docs
- Backlog 3.132 → done; `docs/upgrades.md` (migration 00138, R6, R12).
- [ ] Commit `docs: backlog 3.132 done`.
