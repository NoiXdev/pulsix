# SNMP checks (backlog 3.13d) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The manager polls network devices over SNMP (v2c, v3) with credentials from a new encrypted per-organization secret store, reporting reachability, uptime, chosen OIDs and interface status and throughput.

**Architecture:** `internal/probe` is split into one package per probe and its `Observation` gains Item and Label. A new `internal/orgsecrets` service stores sealed secrets (AAD-bound) behind a write-only API. A new `snmp` descriptor (`Runs: Server`) and probe package (gosnmp) run under the existing probe runner, which looks secrets up and keeps per-slot counter state for rates. Two seed rules, a settings page, docs.

**Tech Stack:** Go, github.com/gosnmp/gosnmp, sqlc + goose on Postgres, huma, React/TypeScript SPA (Vitest).

**Spec:** `docs/superpowers/specs/2026-11-14-snmp-checks-design.md`

## Global Constraints

- Everything in the repository is English; public docs never name the tech stack.
- Tests: never `go test ./...`; per package `go test -race -timeout 900s ./internal/<pkg>/...` (`internal/api` `-timeout 1800s`); `golangci-lint run` and `gofmt -s -l .` clean before every commit.
- Conventional Commits; footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit on the feature branch only; no push, no tag, no `git stash`, no other state-changing git.
- Migrations additive: `00096_org_secrets.sql`, `00097_seed_snmp_rules.sql`; Down for development only.
- Integration tests use `internal/db/testdb`; never dial a default URL or port; Docker only for containers the test itself starts, named `pulsix-sdd-…`, `--rm`; about 50 unrelated containers run on this machine.
- A secret value never appears in a log line, error text, API response, audit entry, observation or test failure message.
- New dependency pinned to an exact release at least two weeks old.
- Regenerate with `make sqlc` (or `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` when the binary is missing) and `cd web && npm run api:gen`; never hand-edit generated code.

## Review Focus

1. A secret value leaking through any path (errors from gosnmp, logs, audit, API, observation error texts, panics) — every task touching secrets tests a distinctive value is absent.
2. A device answering with huge, malformed or hostile data (thousands of interfaces, invalid UTF-8 names, giant strings, wrong types) — Task 4 tests.
3. Rates around counter wrap, counter reset and device reboot never producing negative or absurd values — Task 4/5 tests.
4. Deleting a secret still referenced by any kind of definition (org, group, host, shared projection) — Task 2 tests.
5. The guard: an SNMP target the probe guard refuses must be an error, never `up = 0` — Task 4 test.

---

### Task 1: Split `internal/probe` into one package per probe; Item and Label

**Files:** `internal/probe/` → core (`probe.go`, `guard.go`, helpers) + `internal/probe/{icmp,tcp,http,tls,dns}/`; `internal/probes/runner.go` (pass Item/Label).

**Interfaces — Produces:** `probe.Observation{Metric, Item string; Value *float64; Label *string; Err string}`; a registry `map[string]Func` with `type Func func(ctx context.Context, g *Guard, params map[string]any, address string, secret map[string]string) []Observation` (the `secret` argument is nil for probes that use none; Task 4 uses it); `probe.Run(ctx, g, plugin, params, address, secret)`.

- [ ] Step 1: move the code and tests (no behaviour change); add a test that `Run` dispatches each plugin name and that an unknown plugin fails; a runner test that Item and Label reach `checks.Observation`.
- [ ] Step 2: `go test -race -timeout 900s ./internal/probe/... ./internal/probes/` — PASS (existing tests unchanged apart from package paths and the new `secret` argument).
- [ ] Step 3: commit `refactor(probe): one package per probe, observations with item and label`.

### Task 2: Secret store on the manager

**Files:** create `internal/db/migrations/00096_org_secrets.sql`, `internal/db/queries/org_secrets.sql`, `internal/orgsecrets/{service.go,store_sql.go,validate.go}` + tests; API `internal/api/org_secret_handlers.go` + tests; wiring in `internal/app`; `web/src/api/schema.d.ts` regenerated.

**Interfaces — Produces:**
```go
package orgsecrets
type Secret struct { Name string; FieldNames []string; CreatedAt, UpdatedAt time.Time; UsedBy []string /* check keys, sorted */ }
func (s *Service) Put(ctx, orgID uuid.UUID, name string, fields map[string]string) error
func (s *Service) List(ctx, orgID uuid.UUID) ([]Secret, error)
func (s *Service) Delete(ctx, orgID uuid.UUID, name string) error // ErrNotFound, ErrConflict (message names the check keys)
func (s *Service) Fields(ctx, orgID uuid.UUID, name string) (map[string]string, error) // ErrNotFound; runner only
func (s *Service) Names(ctx, orgID uuid.UUID) ([]string, error) // for MissingSecret
```
Sealing: the manager's box (`crypto.Box.SealAAD`, as `internal/notify` does) over the JSON object, AAD `org_secrets|<org_id>|<name>`. "Used by" comes from a port onto the check definitions (org/group/host and shared projections) whose descriptor `UsesSecret()` and whose `secret` param equals the name.

- [ ] Step 1: failing tests — validation (name pattern, 1–16 fields, field-name pattern, value 1–4096 bytes, UTF-8, NUL), seal/open round trip, AAD binding (a sealed blob under another org or name fails to open), replace keeps created_at and changes updated_at, list carries no values, delete conflict for each binding kind and a shared projection, `Fields` for the runner; integration test for the store; API tests: list/put/delete, permissions (write = the permission notification channel writes use; read = checks read), 404, 409 naming the checks, audit entries containing name and field names but no value (assert a distinctive value is absent everywhere in responses and audit rows).
- [ ] Step 2: run `internal/orgsecrets`, `internal/db/...`, `internal/api` — FAIL.
- [ ] Step 3: implement; regenerate sqlc and the SPA schema.
- [ ] Step 4: PASS. Commit `feat(orgsecrets): an encrypted secret store per organization`.

### Task 3: The `snmp` descriptor, server-side secret warning, seed rules

**Files:** `internal/checkplugins/snmp.go`, `formats.go` (`FormatSNMPOID`), registry, golden tests; `internal/checks` (`MissingSecret` for `Runs: Server` uses the org store's names — a port the checks service already gets or a new one, wired in `internal/app`); `internal/rules/seed.go`, `internal/db/migrations/00097_seed_snmp_rules.sql`, the seed tests (mirror 00094 and its tests); API host checks route if MissingSecret's input changes; SPA fixture `web/src/test/checkPlugins.json`, schema.

- [ ] Step 1: failing tests — descriptor validation (each param rule from the spec's table, the `Check` hook, duplicate oid names, OID arc bounds, leading dot), metrics list golden, item hook (oid names; any item with `interfaces`), rules catalogue rows; `MissingSecret` for a server plugin against the org store (missing → warning; present → none; agent plugin unchanged); seed rules and the migration's rows (integration, NOT EXISTS per org and name, Down removes only seed rows).
- [ ] Step 2: FAIL. Step 3: implement. Step 4: PASS (`internal/checkplugins`, `internal/checks`, `internal/rules`, `internal/db/...`, `internal/api`).
- [ ] Step 5: commit `feat(checks): the snmp check plugin and its default rules`.

### Task 4: The SNMP probe

**Files:** `internal/probe/snmp/` (probe, client wrapper, interfaces, text cleaning, tests), `go.mod`/`go.sum` (gosnmp pinned).

**Interfaces — Consumes:** Task 1's `Func` signature (secret map), the Guard. **Produces:** `snmp.Probe` registered as `"snmp"`; a pure function for rates, e.g. `func rate(prev, cur sample, …) (float64, bool)`, and the counter sample type the runner stores (Task 5) — export what Task 5 needs: `type State` (opaque per-slot state) and `Probe` taking and returning it, or a `Prober` with an explicit state argument. Decide the cleanest shape and document it in the report.

- [ ] Step 1: failing tests per the spec's Tests section for `snmp` (fake agent or fake client interface; every metric; every secret field rule; error texts without values; device data hostile cases: invalid UTF-8, control characters, 1000 interfaces → 128 kept in ifIndex order, duplicate names, unreportable names; no-such-object per OID; guard refusal → errors not `up = 0`; timeout; rates first poll / normal / 32-bit wrap / reset / reboot). Plus the real-daemon test in a throwaway container (`docker run --rm --name pulsix-sdd-snmpd-<random>` with a pinned image, v2c and v3 configured in the test; skip when Docker is unavailable; always stop it).
- [ ] Step 2: FAIL. Step 3: implement. Step 4: PASS `go test -race -timeout 900s ./internal/probe/...`.
- [ ] Step 5: commit `feat(probe): poll devices over SNMP`.

### Task 5: Runner — secrets and rate state

**Files:** `internal/probes/runner.go` (+ a new file for secret lookup/state), `internal/app` wiring (`Deps.Secrets` = orgsecrets service).

- [ ] Step 1: failing tests — a secret-using check looks its secret up and the probe receives it as the separate argument (params unchanged); missing secret → every metric error `secret "<name>" is not defined` and the probe is not called; store error → error observations, no value; the runner keeps per-slot state between polls and drops it when the slot goes; the 128-interface skip is logged at most once per check per hour; nil `Secrets` → "no secret store".
- [ ] Step 2: FAIL. Step 3: implement. Step 4: PASS `./internal/probes/`, `./internal/app/`.
- [ ] Step 5: commit `feat(probes): hand probes their secret and keep state between polls`.

### Task 6: SPA

**Files:** a new `web/src/features/secrets/` (page, dialog, hooks in `web/src/api`), the settings navigation, i18n en/de, the checks dialog texts for `snmp` and its params, the `secret` help text for server plugins, fixtures; tests next to them.

- [ ] Step 1: failing Vitest tests — list renders names/field names/used by; create; replace (every field required again, values never prefilled); delete conflict message naming the checks; `snmp` in the plugin picker with its params; server-plugin secret help points to Settings → Secrets.
- [ ] Step 2: FAIL. Step 3: implement. Step 4: PASS `npx vitest run` (full), `npm run lint`, `npx tsc -b`.
- [ ] Step 5: commit `feat(web): manage secrets and add SNMP checks`.

### Task 7: Documentation

`docs/BACKLOG.md` (3.13d done; 3.6h's SNMP half pointed here), `docs/upgrades.md` (00096, 00097, new dependency), noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-snmp-checks` from `docs/pulsix-local-checks`: SNMP checks page, secrets in the organization settings, check-plugins reference, changelog; build (248 + new pages). Commit in both repos.
