# Pulsix — project conventions

Pulsix is a multi-tenant, self-hostable server management (Ansible) and monitoring
platform by NoiXdev. Design spec: `docs/superpowers/specs/`, plans: `docs/superpowers/plans/`,
backlog: `docs/BACKLOG.md`, brand: `brand/`.

## Language

- Chat with the user in German. Everything in the repository is English: code,
  comments, commit messages, docs, identifiers.

## Code structure (testability first)

- **Layers, strictly:** `internal/api` (HTTP transport: decode, call a service, encode)
  → `internal/<domain>` services (business rules, no HTTP types, no SQL) → stores
  (sqlc-generated `internal/db/gen` behind a small interface per domain).
- Handlers contain no business logic and never touch `gen.Queries` directly.
- Services depend on **interfaces** declared next to the service (e.g.
  `type Store interface { … }`), so unit tests use fakes without a database.
  Integration tests exercise the real store through `internal/db/testdb`.
- Dependency injection through constructors (`NewService(store, clock, …)`); no
  package-level state, no `init()` side effects. Time and randomness are injected
  (`Clock`, `Rand`) when behaviour depends on them.
- One responsibility per file; a file over ~400 lines is a signal to split.
- Every package exposes the minimum: unexported by default.
- Errors: wrap with context (`fmt.Errorf("claim job: %w", err)`); services return
  sentinel errors (`ErrNotFound`, `ErrForbidden`) that handlers map to HTTP.
- SPA mirrors this: `web/src/api` (generated client + hooks) → `web/src/features/<domain>`
  (components + logic) → `web/src/ui` (presentational, no data fetching). ESLint enforces
  the last edge: a value import from `src/api` inside `src/ui` fails lint (type-only
  imports of the generated schema types are fine).

## Testing

- No feature without tests. TDD: failing test first, then the minimal implementation.
- Table-driven unit tests for logic; integration tests against the Postgres container;
  API tests through `internal/api/apitest`; SPA with Vitest; E2E with Playwright.
- Coverage gates: Go ≥ 80 % (auth, tenancy, db ≥ 90 %), SPA ≥ 70 %.
- Run the race suite as `make test`, never one whole-module `go test ./...`. `scripts/test-groups.sh` defines the groups and their timeouts — `internal/e2e` on its own invocation or its SSH loopback fixtures time out under load (backlog 2.83), `internal/api` at 1800 s because it is the slowest package by far (about 160 s under `-race` since backlog 3.46, over 1 000 s before), everything else at 900 s (backlog 3.21). `go test`'s 600 s per-package default panics the whole invocation rather than failing one test, so never run these without a timeout. That and `golangci-lint run` must pass before every commit; since backlog 3.25 the linter set carries gofmt (simplifying, so `gofmt -s -l .` is the equivalent by hand), and CI keeps its own cheap `gofmt -s -l .` step ahead of it (backlog 2.59).

## Git

- Conventional Commits, footer `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Commit or push only when the user asks. Repo is private until v1.0.
- Migrations are additive only; the migration, upgrade and version-skew rules are in `docs/upgrades.md`.

## Documentation

- Internal docs (specs, plans, ADRs, backlog) live in this repo under `docs/`.
- **End-user documentation lives in the NoiXdev docs repo** (`NoiXdev/noix-docs`,
  published at https://docs.noix.dev/) under the Pulsix section. Whenever a feature
  changes user-visible behaviour (GUI, API, agent config, install), update the
  matching page there in the same work item and mention the docs commit in the PR.
  Work on the docs repo happens on a feature branch until the feature ships.
- Public texts (docs site, public README, landing copy) never name the tech stack.
