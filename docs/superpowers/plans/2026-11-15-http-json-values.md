# HTTP JSON values (backlog 3.6h) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `http` check extracts numbers and texts from a JSON response by JSONPath and can authenticate with a secret from the organization's store.

**Architecture:** Descriptor gains `json_values`, `secret` and two metrics; the http probe parses the capped body once, evaluates RFC 9535 paths with the library the triggers already use, and sets credentials that never cross origins on redirects; the runner fails only the check's own metrics when the secret is missing.

**Tech Stack:** Go, github.com/theory/jsonpath (already a dependency), React/TypeScript SPA.

> **Superseded (library):** after the Task 2 review the spec was amended: paths are a bounded RFC 9535 subset parsed and evaluated by Pulsix's own `internal/jpath`, not by `github.com/theory/jsonpath` (which stays only in `internal/triggers`). The Architecture and Tech Stack lines above, and the tasks below that name the library, describe the plan as first written; the spec's Descriptor and Probe sections are authoritative.

**Spec:** `docs/superpowers/specs/2026-11-15-http-json-values-design.md`

## Global Constraints

- Repository English; public docs never name the tech stack.
- Never `go test ./...`; per package `go test -race -timeout 900s` (`internal/api` 1800 s); `golangci-lint run`, `gofmt -s -l .` clean.
- Conventional Commits; footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; commit on the branch only; no push, tag, stash or other state-changing git.
- No secret value in any error, log, observation or test failure output.
- Tests never dial default URLs/ports; in-test servers on loopback with the probe test guard; touch no foreign Docker container.

## Review Focus

1. Credentials leaking across a redirect to another origin or to plain http — Task 2 tests.
2. Hostile JSON: deep nesting, huge numbers, giant strings, many matches — Task 2 tests (bounded by the 1 MiB cap; parsing must not blow the stack).
3. A missing secret producing item-less phantom rows — Task 2/3 tests.
4. HEAD with json_values, duplicate names, invalid paths — Task 1 tests.
5. body_contains and json_values together reading the body once — Task 2 test.

---

### Task 1: Descriptor, format and SPA twin

**Files:** `internal/checkplugins/http.go`, `formats.go` (`FormatJSONValue`), golden tests, rules catalogue rows, API enums/openapi as the "Adding a plugin" note lists for metric/param changes; SPA fixture `web/src/test/checkPlugins.json`, en/de texts, `paramFormats.ts` twin, schema regen (`cd web && npm run api:gen`).

- [ ] Failing tests per the spec's Descriptor section (valid/invalid entries incl. length 512, unique names, HEAD refused, metrics golden, ItemAsked, secret optional); SPA format twin vectors.
- [ ] Implement; PASS for `internal/checkplugins`, `internal/checks`, `internal/rules`, `internal/api`, `internal/probe/...` (golden), SPA vitest/lint/tsc.
- [ ] Commit `feat(checks): JSON values and credentials on the http check`.

### Task 2: Probe and runner

**Files:** `internal/probe/http/` (json.go, auth.go, redirect policy, tests), `internal/probe/http` `FailAll(params, msg)`; `internal/probes` dispatch of a plugin-specific FailAll (generalise what 3.13d did for snmp rather than adding another special case: e.g. an optional per-plugin "metrics for these params" function in the probe registry).

- [ ] Failing tests per the spec's Probe, Runner and Tests sections.
- [ ] Implement; PASS `./internal/probe/...`, `./internal/probes/`, `./internal/app/`.
- [ ] Commit `feat(probe): read JSON values and send credentials on http checks`.

### Task 3: Documentation

`docs/BACKLOG.md` (3.6h done), `docs/upgrades.md` if anything operator-visible changed (no migration expected), noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, branch `docs/pulsix-http-json-values` from `docs/pulsix-snmp-checks`: http check reference (JSON values, credentials, redirect rule), organization secrets page (http forms), changelog. Build (250 pages before). Commit in both repos.
