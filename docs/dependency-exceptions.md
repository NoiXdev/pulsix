# Dependency exceptions

The project rule is that every dependency runs on its newest stable release, or the
active LTS line where one exists. This file lists the deliberate exceptions, why each
one exists, and the condition that lifts it. Review it whenever a dependency is
upgraded; an exception without a live reason is a bug.

| Dependency | Pinned | Newest stable | Reason | Lift when |
|---|---|---|---|---|
| `typescript` (web) | 5.9.x | 7.0.x | `openapi-typescript`, which generates the API client, declares a peer range of `typescript ^5.x`. TypeScript 6 and 7 are outside it. `typescript-eslint` is not the blocker: it already supports 6.0. | `openapi-typescript` widens its peer range to include the newest TypeScript major. |

Recorded 2026-09-11 during v0.1 Task 15.
