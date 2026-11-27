# Assertions on snmp checks (backlog 3.134) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 3.134: "Assertions on snmp checks — From 3.87. The 3.87 grammar for `snmp`: subjects for the named `oids` values (`value("name")`, numeric or text by the value's type), `response_ms` and `uptime`; decide what an actual may show of a device's values, since v3 secrets and community strings must never appear".

**Architecture:** 3.87's machinery unchanged (plan `2026-11-26-assertions-other-plugins-3.87.md`): `internal/probe/assert` gains the `SNMP` dialect, the snmp probe judges entries through `internal/probe/judge` after its poll, the descriptor gains `assertions` (format `snmpAssertion` with its TypeScript twin), migration 00135 and `rules.SeedRules()` seed the warn and crit rules, the SPA's editor and host-page table take `snmp`. The one new thing is a subject whose kind is not fixed: `value("name")` compares numbers or text depending on its operator and operand.

**Spec:** the backlog row above, 3.87's plan and 3.13b's spec section 3, whose grammar, severities, metrics and evaluation rules apply except where a ruling says otherwise.

## Global Constraints
- An snmp check without `assertions` polls and reports exactly as before.
- One migration (00135), additive: seeded rules only. No proto change, no dependency.
- Nothing the secret holds reaches evaluation: the judge sees only the poll's decoded values.

## Rulings
- R1 Subjects: `value("name")`, `response_ms` (numeric, `snmp.response_ms`) and `uptime` (numeric, seconds, `snmp.uptime_seconds`). `value`'s argument follows the oids name rule; the descriptor refuses a `value("x")` no `oids` entry names ("assertion "a" reads value("x"), which no oids entry names") — cost if wrong: none, renaming an oid needs the assertion renamed too, which the refusal says.
- R2 `value` takes every numeric and text operator (`<`, `<=`, `>`, `>=`, `in`, `contains`, `!contains`, `matches`, `!matches`, `==`, `!=`); `==` and `!=` compare numbers when the operand is a number and text when it is a JSON string. No `exists`: a missing object fails every operator (R4). The comparison's kind is the assertion's (`Assertion.Numeric()`), so the canonical expression keeps what was written — cost if wrong: one more grammar rule to learn; the alternative (two subjects) splits one value into two names.
- R3 Runtime typing: a number compared as text is compared by its decimal spelling (Go's shortest form, as the actual prints it); text compared as a number is read as a decimal number when it is one (`numberPattern`, after cleaning, so `"0.15"` from UCD's `laLoad` works), else the entry reads the error "the device answered with text, not a number: compare it with a quoted string" (unknown, not a failure). The text compared is the value as `snmp.text` shows it before its `(empty)` placeholder: cleaned (`probe.CleanText`, NUL padding and control characters gone), at most 512 bytes — what an operator sees is what is compared. Cost if wrong: a `contains` past 512 bytes of a device string misses.
- R4 Missing values: an oids entry the device does not have (`noSuchObject`/`noSuchInstance`/`endOfMibView`) and a missing `sysUpTime` fail every operator with the actual `absent` (3.87 R4's numeric rule; a type-less value has no text rule to apply). Any other value error (unsupported type, invalid OID, no answer to a later request, malformed answer, an error status) reads as that same fixed error on both assertion metrics — cost if wrong: an alert for a missing object instead of an unknown reading, which is what a stated expectation should do.
- R5 Actuals show the value by its type: a number unquoted (as `snmp.value` holds it), text quoted, cleaned and cut to 128 bytes (`judge.TextActual`), `absent`. Secrets: the community and v3 credentials live in the organization's secret, go to the client only and are never values; evaluation receives the decoded values and nothing else, errors are the probe's fixed strings. A device that returns its community as an OID's value (an operator pointing an oid at it) shows it like any other value — the same as `snmp.text` already does. Pinned by `TestSNMPAssertionsNeverSeeTheSecret`.
- R6 Interfaces are out of scope: interface items are named by the device, unknown when the check is saved, and `snmp.if.status` already alerts per interface through the rules. Recorded as left out.
- R7 Items: as http's `json_values` and `assertions`, the oids names and the assertion names are one namespace — the descriptor refuses an assertion named like an oids entry. An interface the device names like an assertion shares the item, on other metrics, harmlessly. `ItemParam` stays `oids`; the item hook asks for the assertion names as well.
- R8 Evaluation runs on an up poll only, after every request; a down poll reports nothing on the assertions (the seeded `SNMP device unreachable` rule alerts); a poll that cannot be attempted (bad params, unusable secret, refused target) puts its error on both metrics of every entry (`FailAll`).
- R9 Skew: snmp names a secret, so it is probed from the manager only (`ProbedOnlyFromManager`, vantage groups refused) — no agent ever answers an snmp round, and 3.87's `judge.Unevaluated` covers snmp anyway through `DialectOf`. An older manager still running during a rolling upgrade polls without evaluating; the items stay without a reading until a new manager polls ("stop every manager instance first", `docs/upgrades.md`).
- R10 Rules: migration 00135 and `SeedRules()` seed `SNMP assertion failed` (warn) and `SNMP assertion failed (critical)`, 3.87's shape; an organization holding a rule of the name keeps it.

## Review Focus
1. Typed value: number vs text per operator, text that is a number, text that is not (an error, not a failure), absent — `TestSNMPValueAssertions`.
2. Secrets never in an actual or error — `TestSNMPAssertionsNeverSeeTheSecret` (v2c community and v3 passphrases over every observation).
3. Grammar: `value` with both operand kinds, refusals (`exists`, a bad name, http subjects) — `TestDialectParse`, `TestDialects`.
4. Save-time: `value("x")` naming no oids entry, an assertion named like an oid — `TestSNMPAssertionsCheck`.
5. Unattempted poll covers the assertion items — `TestFailAllReportsTheDescriptorsMetrics`, `TestSNMPFailAllCoversAssertions`.
6. Seeds match between migration and `SeedRules()` — `TestRulesBackfillMatchesSeedRules`, `TestSNMPAssertionRulesMigrationMatchesSeedForOrg`.

## Task 1: Dialect
**Files:** `internal/probe/assert/{assert.go,parse.go,dialect.go,judge.go,dialect_test.go}`.
**Interfaces:** `assert.SNMP`, `SubjectValue`, `SubjectUptime`, `(Assertion).Numeric()`.
- [ ] Commit `feat(probe): assertion dialect for snmp`.

## Task 2: Probe
**Files:** `internal/probe/snmp/{snmp.go,values.go,assert.go,assert_internal_test.go}`, `internal/probe/judge/actual.go` (uptime, value formatting).
**Tests:** `TestSNMPValueAssertions`, `TestSNMPHostAssertions`, `TestSNMPAssertionsNeverSeeTheSecret`, `TestSNMPFailAllCoversAssertions`.
- [ ] Commit `feat(snmp): assertions on values, response time and uptime`.

## Task 3: Descriptor, format, API
**Files:** `internal/checkplugins/{snmp.go,formats.go,testdata/formats.json}`, `internal/api/check_plugin_schema.go` (+ test), `internal/rules/catalog_test.go`, `internal/probe/all/all_test.go`, `web/src/test/checkPlugins.json`.
**Tests:** `TestSNMPAssertionsCheck`, `TestAssertionFormats` vectors, descriptor goldens.
- [ ] Commit `feat(checks): assertions parameter on snmp`.

## Task 4: Rules and alert detail
**Files:** `internal/db/migrations/00135_snmp_assertion_rules.sql`, `internal/rules/{seed.go,*_test.go}`, `internal/app/{alerts_detail_ports_test.go,app_test.go}`, `internal/api/rule_handlers_test.go`.
- [ ] Commit `feat(rules): seed assertion rules for snmp`.

## Task 5: SPA
**Files:** `web/src/features/checks/{assertionFormat,assertionRows,AssertionEditor,paramFormats}.ts(x)`, i18n.
**Tests:** `assertionFormat.test.ts`, `assertionRows.test.ts`, `AssertionEditor.test.tsx`, `paramFormats.test.ts`, `httpCheck.test.ts`.
- [ ] Commit `feat(web): assertion editor and table for snmp`.

## Task 6: Docs
**Files:** `docs/BACKLOG.md`, `docs/upgrades.md`.
- [ ] Commit `docs: backlog 3.134 done`.
