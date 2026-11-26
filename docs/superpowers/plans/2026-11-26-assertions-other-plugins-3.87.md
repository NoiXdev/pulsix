# Assertions on tcp, dns and tls (backlog 3.87) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 3.87: "Assertions on plugins other than http — From 3.13b. The assertion grammar and the severity per statement for `tcp`, `dns`, `tls` and `snmp`."

**Architecture:** 3.13b's grammar (`internal/probe/assert`, spec `docs/superpowers/specs/2026-11-16-web-assertions-design.md`, section 3) gains *dialects*: one list of subjects per plugin, a subject's kind (numeric, text, presence) being the same in every dialect that has it. `assert.Parse` stays http's dialect; `assert.DialectOf(plugin).Parse` serves the others. A new package `internal/probe/judge` holds what every probe needs to evaluate entries (reading the `assertions` param, the regular-expression budget, the actual's formatting, the two observations per item); the tcp, dns and tls probes fill in their subjects from what they measure. The descriptors gain the `assertions` parameter (new formats `tcpAssertion`, `dnsAssertion`, `tlsAssertion`, each with a TypeScript twin), `<plugin>.assertion` and `<plugin>.assertion_actual`, and item hooks; the manager seeds the warn and crit rules per plugin, the alert detail reads any `<plugin>.assertion`, the probe runner's vantage combining refuses to let an older agent's round silently drop assertions, and the SPA's editor and host-page table work per plugin.

**Spec:** the backlog row (above) and 3.13b's spec, section 3, whose grammar, severities, result metrics and evaluation rules apply unchanged except where a ruling below says otherwise.

## Global Constraints
- An http check behaves exactly as before: same grammar, same messages (the unknown-subject error only gains a list of the subjects), same metrics.
- Nothing changes for a tcp, dns or tls check without `assertions`, except the descriptor's new parameter and metrics.
- One migration (00134), additive: seeded rules only. No proto change, no new dependency.
- Tests per package (`go test -race -timeout 900s ./internal/probe/... ./internal/checkplugins/... ./internal/probes/... ./internal/rules/... ./internal/app/...`, `internal/api` with `-run`); `golangci-lint run` and `gofmt -s -l` clean; SPA `npm run lint`, `npx tsc -b`, `npx vitest run` on touched files.

## Rulings
- R1 snmp is split out as backlog 3.134: its subjects are per-OID typed values (`value("name")`), the probe runs only on the manager and its v3 secrets need their own review of what an actual may show. tcp, dns and tls make one coherent branch — cost if wrong: snmp users wait for the follow-up.
- R2 Dialects, not new grammars: the entry syntax, operators, `in a..b`, JSON string operands, ` @warn`/` @crit`, 32 entries of 512 bytes, the RE2 bounds and per-check budget are 3.13b's. A subject name means the same in every dialect that has it (`response_ms`, `cert_days`, `cert_issuer`), so `Subject.Numeric()` stays global. An unknown subject's error lists the dialect's subjects — a user typing an http subject into a tcp check learns which exist.
- R3 tcp subjects: `connect_ms` (numeric, `tcp.connect_ms`), `response_ms` (numeric: from the dial's start to the end of the response read), `response` (text: the first line the target sent after `send`, without its line break, at most 4 KiB). Only a `response`/`response_ms` assertion or `expect` makes the probe read; the read now stops once `expect` is found (when set) and a line break arrived (when a response subject asks), or at 4 KiB, close or the timeout. `send` is never a subject and never part of an actual. Why the first line: banners (SSH, SMTP, FTP, POP3, Redis replies) are one line, and stopping there keeps a check from waiting for its timeout on a server that keeps the connection open — cost if wrong: a multi-line greeting's later lines are only visible to `expect`.
- R4 A numeric subject the attempt has no value for (tcp's `response_ms` when nothing arrived) fails every operator with the actual `absent`; text subjects keep 3.13b's absent rule (`!=` and `!exists` pass) — "respond within 500 ms" must not pass when there was no response.
- R5 dns subjects: `resolve_ms` (numeric, `dns.resolve_ms`), `answer_count` (numeric), `answers` (text: the answers sorted and joined with ", ", names lower-cased without the trailing dot, addresses in canonical form, TXT strings as received), `answer("x")` (presence only, `exists`/`!exists`: an answer equal to x by `expect`'s comparison — case-insensitive, without a trailing dot, addresses as addresses). No rcode: an attempt is up only with NOERROR and answers (NXDOMAIN and SERVFAIL are `dns.failure` already). No TTL: the system resolver does not expose it; reading it would need an own DNS client — recorded as left out in the backlog row.
- R6 tls subjects: `handshake_ms`, `cert_valid` (numeric 0/1, `tls.cert_valid`), `cert_days`, `cert_issuer` (as http's), `cert_subject` (text: the leaf subject's common name, else its first organization, else the DN, cleaned like the issuer), `cert_sans` (text: the leaf's DNS names then IP addresses, in certificate order, joined with ", "; absent when it has none), `version` (numeric: 1.0, 1.1, 1.2 or 1.3; the actual is printed with one decimal) and `cipher` (text: the suite's IANA name). No new metrics: the subjects read what the handshake already holds — cost if wrong: a user wanting a version graph adds a follow-up.
- R7 Results follow 3.13b: per entry, item = its name, `<plugin>.assertion` (`pass`, `warn`, `crit`) and `<plugin>.assertion_actual` (same formatting, `absent`); evaluated only while `<plugin>.up` is 1; nothing on a down attempt (the seeded `<plugin>.up` rule alerts with the reason); an attempt that could not be made (bad params, guard refusal) puts its error on both metrics of every entry. The descriptors use `assertions` as their `ItemParam` with `HostItemAlwaysAsked` and an `ItemAsked` hook (no entries asks for no item).
- R8 Secrets: tcp, dns and tls take no secret. An actual quotes only what the target sent (a reply line, DNS answers, certificate fields), cut to 128 bytes, as 3.13b's body and header actuals do; `send` and `server_name` are the operator's own parameters and never an actual.
- R9 Formats: three new formats (`tcpAssertion`, `dnsAssertion`, `tlsAssertion`) rather than one format with a plugin argument — the format machinery validates a value without its plugin, and each gets its own vectors and TypeScript twin.
- R10 Rules: migration 00134 and `rules.SeedRules()` seed six rules in 3.13b's shape — `TCP assertion failed` (warn) and `TCP assertion failed (critical)`, the same for DNS and TLS — so a failed assertion alerts without setup, as for http; existing organizations get them unless they hold a rule of the name.
- R11 Alert detail: `<expression> — saw <actual>` for `<plugin>.assertion` of every plugin with assertions, the expression parsed in the check's own dialect.
- R12 Version skew. Agents: an older agent's tcp, dns or tls probe ignores `assertions`, so the representative round of a vantage check could simply lack the items. The manager's combining (`internal/probes`) therefore marks every entry missing from an up representative round as the error "the vantage point that answered did not evaluate this assertion; update its agent" (`judge.Unevaluated`): the item reads unknown, with a reason, instead of going stale and vanishing. A round split across two result messages could show this for one combine — cost: one unknown reading until the next combine. Managers: an older manager still running during a rolling upgrade probes without assertions (its validation refuses the parameter, but it may probe checks a newer manager saved); the items go stale until a new manager probes — documented in `docs/upgrades.md` with the usual "stop every manager instance first". snmp never runs on agents.
- R13 http keeps its own evaluation code; the actual formatting (`numberActual`, `textActual`) moves to `judge` and http calls it — one formatting for every plugin, no change to http's tests.
- R14 SPA: the grammar twin takes the plugin (`parseAssertion(entry, plugin)`); the editor offers the plugin's subjects; the host page's assertion table (with 3.89's actions) shows for every plugin with assertions, the http timing bar stays http's.

## Review Focus
1. tcp read rule: a server that keeps the connection open must not hold the probe to its timeout; `expect` must still find text past the first line — `TestTCPResponseAssertions` (line, CRLF, no line break then close, expect on line 2 with a response subject, nothing sent then timeout).
2. Absent numeric fails every op (`TestTCPResponseMsAbsent`), absent text keeps 3.13b's rule (`TestJudgeAbsentText`).
3. dns `answer("…")` compares like `expect` (case, trailing dot, IPv6 spellings) — `TestDNSAssertions`.
4. Skew: an up round without assertion items becomes an error per entry, a down round or a round with every item stays as it is — `TestCombineMarksUnevaluatedAssertions`, `TestUnevaluated`.
5. Grammar: http entries parse exactly as before, other dialects refuse http's subjects and vice versa — `TestDialects`, the existing `TestParseAccepts`/`TestParseRefuses` unchanged.
6. The seeded rules match between migration and `SeedRules()` — `TestRulesBackfillMatchesSeedRules`, `TestAssertionRulesMigrationMatchesSeedForOrg`.

## Task 1: Dialects
**Files:** `internal/probe/assert/{assert.go,parse.go,dialect.go,dialect_test.go}`.
**Interfaces:** `type Dialect`, `assert.HTTP/TCP/DNS/TLS`, `DialectOf(plugin) (Dialect, bool)`, `(Dialect).Parse`, `(Dialect).Subjects()`, new subjects `connect_ms response response_ms resolve_ms answer_count answers answer handshake_ms cert_valid cert_subject cert_sans version cipher`, `ValidName`.
**Tests:** `TestDialects`, `TestDialectParse`.
- [ ] Commit `feat(probe): assertion dialects for tcp, dns and tls`.

## Task 2: Shared evaluation
**Files:** `internal/probe/judge/{judge.go,actual.go,judge_test.go}`, `internal/probe/http/assert_actual.go` (delegates).
**Interfaces:** `Entry`, `Entries(d, params)`, `Verdict`, `Failed`, `Work`, `(w *Work) Text`, `Number`, `Absent`, `Observe(plugin, entries, f)`, `FailAll(plugin, entries, msg)`, `Unevaluated(plugin, params, obs)`, `NumberActual`, `TextActual`.
**Tests:** `TestEntries`, `TestObserve`, `TestJudgeAbsentText`, `TestNumberActual`, `TestUnevaluated`.
- [ ] Commit `feat(probe): shared assertion evaluation`.

## Task 3: tcp, dns, tls probes
**Files:** `internal/probe/{tcp,dns,tls}/*.go`, `internal/probe/all/all.go` (FailAll for the three), `internal/probe/probe.go` (`SubjectLabel`).
**Tests:** `TestTCPAssertions`, `TestTCPResponseAssertions`, `TestTCPResponseMsAbsent`, `TestDNSAssertions`, `TestTLSAssertions`, `TestFailAllCoversAssertions`.
- [ ] Commit per probe: `feat(tcp): assertions`, `feat(dns): assertions`, `feat(tls): assertions`.

## Task 4: Descriptors, formats, API
**Files:** `internal/checkplugins/{tcp,dns,tls,formats,assertions}.go`, `testdata/formats.json`, `internal/api/check_plugin_schema.go`, `internal/rules/catalog_test.go`, SPA fixture, `web/src/api/schema.d.ts`.
**Tests:** `TestAssertionFormats`, `TestSyntheticAssertionItems`, existing descriptor goldens.
- [ ] Commit `feat(checks): assertions parameter on tcp, dns and tls`.

## Task 5: Skew in combining
**Files:** `internal/probes/vantages.go`.
**Tests:** `TestCombineMarksUnevaluatedAssertions`.
- [ ] Commit `feat(probes): flag assertions an older vantage did not evaluate`.

## Task 6: Rules and alert detail
**Files:** `internal/db/migrations/00134_assertion_rules.sql`, `internal/rules/seed.go`, `internal/rules/*_test.go`, `internal/app/alerts_detail_ports.go`.
**Tests:** `TestAssertionRulesMigrationMatchesSeedForOrg`, `TestRulesBackfillMatchesSeedRules`, alert detail tests for tcp.
- [ ] Commit `feat(rules): seed assertion rules for tcp, dns and tls`.

## Task 7: SPA
**Files:** `web/src/features/checks/{assertionFormat,assertionRows,AssertionEditor,paramForm,paramFormats,ParamFields}.ts(x)`, `web/src/features/hosts/{httpCheck.ts,HostCheckHttp.tsx,HostCheckDetail.tsx}`, `web/src/features/checks/syntheticLabels.ts`, `web/src/i18n/{en,de}/checks.ts`.
**Tests:** `assertionFormat.test.ts`, `assertionRows.test.ts`, `AssertionEditor.test.tsx`, `httpCheck.test.ts`, `paramFormats.test.ts`.
- [ ] Commit `feat(web): assertion editor and table for tcp, dns and tls`.

## Task 8: Docs
**Files:** `docs/BACKLOG.md` (3.87 done, 3.134 new), `docs/upgrades.md`.
- [ ] Commit `docs: backlog 3.87 done`.
