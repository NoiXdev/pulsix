# Dependency exceptions

The project rule is that every dependency runs on its newest stable release, or the
active LTS line where one exists. This file lists the deliberate exceptions, why each
one exists, and the condition that lifts it. Review it whenever a dependency is
upgraded; an exception without a live reason is a bug.

| Dependency | Pinned | Newest stable | Reason | Lift when |
|---|---|---|---|---|
| `typescript` (web) | 5.9.x | 7.0.x | `openapi-typescript`, which generates the API client, declares a peer range of `typescript ^5.x`. TypeScript 6 and 7 are outside it. `typescript-eslint` is not the blocker: it already supports 6.0. | `openapi-typescript` widens its peer range to include the newest TypeScript major (re-checked 2026-11-09: 7.13.0 still declares `^5.x`). Dependabot ignores TypeScript majors (`.github/dependabot.yml`); drop that rule when lifting. |
| `minimatch` (web, under `eslint`, `@eslint/config-array`, `typescript-eslint`) | overridden to `^11.0.5` (`overrides` in `web/package.json`) | 11.0.x | Not a pin but the opposite: an override past the declared range. Every 10.x release is affected by GHSA-g3hv-75wg-m249 (pattern amplification, a CPU denial of service) and the fix exists only in 11.0.1+, while `eslint` 10.14.0 and its config packages declare `^10.2.x`. Lint-time only; the patterns are the repository's own config. Checked 2026-11-09: with the override `eslint .` lints the same 651 files with the same (zero) findings. `@redocly/openapi-core`'s `minimatch` 5.x is outside the advisory and untouched. | `eslint` and `@eslint/config-array` declare a `minimatch` range that includes a fixed release; then remove the `overrides` entry. |

Recorded 2026-09-11 during v0.1 Task 15.
