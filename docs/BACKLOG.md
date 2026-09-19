# Pulsix backlog

Planning backlog for Pulsix, the NoiXdev server management and monitoring platform.
One line per deliverable, grouped by milestone. Update the status column as work moves.

Status legend: `idea` · `mockup` · `spec` · `planned` · `in progress` · `done`

Last updated: 2026-09-11

## Milestones

| Version | Theme | Goal |
|---------|-------|------|
| v0.0 | Brand & design | Name, CI, logo, GUI mockups, architecture spec |
| v0.1 | Core | Orgs, auth, hosts, tags, REST API + explorer, single-node compose |
| v0.2 | Inventory & Ansible | Groups, variable schema builder, Git repos, job templates, worker, runs |
| v0.3 | Agent & monitoring | Cross-platform agent, enrollment, checks, time series, graphs |
| v0.4 | Alerting | Monitoring groups, rules, schedules, channels, escalation, maintenance |
| v0.5 | Status pages | Public pages, components, incidents, subscriptions |
| v0.6 | Scale & HA | Multi-instance manager, zone workers, horizontal scaling |
| v1.0 | Release | Hardening, docs, upgrade path, public launch |

## v0.0 · Brand & design

| # | Item | Status | Notes |
|---|------|--------|-------|
| 0.1 | Name "Pulsix", GitHub org NoiXdev, domain check | done | pulsix.dev / .io free |
| 0.2 | Logo direction A (Heartbeat Hex), family cyan | done | `brand/` SVG set, tokens |
| 0.3 | Mockup: dashboard | mockup | artifact "Pulsix App" |
| 0.4 | Mockup: host detail | mockup | artifact "Pulsix App" |
| 0.5 | Mockup: host list with managed/monitored capability columns | mockup | |
| 0.6 | Mockup: inventory groups + variable schema builder + assignment form | mockup | |
| 0.7 | Mockup: Ansible run view with live log | mockup | job template form still open |
| 0.8 | Mockup: monitoring groups (tag selector) + rule editor (form/expression, effective rules) | mockup | |
| 0.9 | Mockup: agent enrollment flow | mockup | |
| 0.10 | Mockup: status page (public) + status page admin | mockup | |
| 0.11 | Design spec (architecture, data model, protocols) | spec | `docs/superpowers/specs/2026-09-10-pulsix-architecture-design.md`, in review |
| 0.13 | Mockups: setup wizard, account security, members/ACL, activity feed, credentials, playbooks/repos, schedules + webhook triggers, SSH access step, workers & zones, synthetic web/port check, monitoring group hierarchy | mockup | canvas pages "Core & monitoring" and "Admin & Ansible" |
| 0.12 | Implementation plan v0.1 Core | planned | `docs/superpowers/plans/2026-09-10-v0.1-core.md`; later milestones get their own plans |

## v0.1 · Core

| # | Item | Status | Notes |
|---|------|--------|-------|
| 1.1 | Repo layout: Go monorepo (manager, worker, agent, proto), `web/` React SPA | planned | |
| 1.1a | CI: lint, unit + integration tests on PR, coverage gates (Go ≥ 80 %, critical ≥ 90 %, SPA ≥ 70 %), Conventional Commits check | planned | test policy: no feature without tests |
| 1.1b | E2E harness: Compose stack in CI, Playwright, Go scenario runner, mock SMTP/webhook, SSH + bastion + agent containers | planned | scenarios listed in spec §18 |
| 1.2 | PostgreSQL + TimescaleDB schema, migrations | planned | single DB for everything |
| 1.3 | Organizations, users, roles (owner, admin, operator, viewer) | planned | multi-tenant from day one |
| 1.4 | Auth: password + TOTP MFA | planned | |
| 1.5 | Auth: passkeys (WebAuthn), recovery codes | planned | go-webauthn |
| 1.6 | API tokens (per user / per org, scoped) | planned | |
| 1.7 | REST API, OpenAPI 3.1 spec generated from code | planned | API-first, SPA uses only the public API |
| 1.8 | API explorer in the GUI (Scalar or Swagger UI) | planned | |
| 1.9 | Hosts: CRUD, zones, capability flags `managed` / `monitored` | planned | independent, per host |
| 1.10 | Host tags (key=value), manual + auto from facts | planned | link for monitoring groups and filters |
| 1.11 | Audit log | planned | |
| 1.12 | SPA shell: sidebar, org switcher, ⌘K search, zone filter, notifications | planned | |
| 1.13 | Docker Compose single-node install (manager, worker, db) | planned | `docker compose up` |
| 1.14 | Light theme | idea | dark-first, light later |
| 1.15 | Rate limiting on auth endpoints (login, TOTP verify, passkey finish, invite accept) | planned | before v0.1 release |
| 1.19 | Account recovery when the second factor is lost: a recovery code must also authorise re-enrolling or disabling TOTP, otherwise the only way back is an administrator reset | planned | found during v0.1 Task 7 review |
| 1.16 | User management: invitations by e-mail, roles (owner/admin/operator/viewer), remove, instance-admin deactivate | planned | plan v0.1 Task 10 |
| 1.17 | Activity feed per org and per user from the audit log, filters by actor/object | planned | plan v0.1 Task 11 |
| 1.18 | First-run setup wizard: instance admin, first org, SMTP, forced second-factor enrolment | planned | plan v0.1 Task 12 |

## v0.1 follow-ups from the final branch review

Items the final whole-branch review deferred. The first group blocks a `v0.1.0` release tag;
the rest are follow-ups. Recorded 2026-09-11.

| # | Item | Status | Notes |
|---|------|--------|-------|
| 1.20 | Rate limiting on login, TOTP verify, passkey finish and invite accept | planned | **release blocker** (with 1.15) |
| 1.21 | Reapers for expired `webauthn_ceremonies` and `sessions` rows | planned | **release blocker**: unauthenticated write path grows without bound |
| 1.22 | Throttle `TouchLastUsed`; skip the session lookup for static assets | planned | **release blocker**: per-request write and read amplification |
| 1.23 | Batch `set_config` with the statement in one round trip for org-scoped queries | planned | today four round trips per scoped statement |
| 1.24 | SMTP settings `PUT`: an omitted password keeps the stored one | planned | today saving another field wipes the password |
| 1.25 | Invite cleanup on mail failure: detached context and a dial timeout; do not delete after DATA was accepted | planned | |
| 1.26 | Make TOTP confirmation and the session upgrade atomic | planned | recoverable today, but the recovery codes are lost |
| 1.27 | Re-enrolling TOTP keeps the confirmed secret until the new one is confirmed | planned | today an abandoned re-enrolment leaves the account password-only |
| 1.28 | Step-up (fresh second factor) for passkey registration and deletion | planned | |
| 1.29 | Security headers (CSP, frame-ancestors, nosniff, referrer policy); gate `/schemas/*` like the OpenAPI document | planned | |
| 1.30 | Composite foreign keys tying `host_tags.org_id` and `hosts.zone_id` to their owners | in progress | composite keys for all v0.2a references; host_tags and hosts.zone_id remain |
| 1.31 | `If-Match` optimistic concurrency on config objects, starting with host updates | in progress | If-Match on inventory groups and memberships; host updates remain |
| 1.32 | Release workflow publishes only commits on `main` with green CI; `ci.yml` permissions block and SHA-pinned actions | planned | |
| 1.33 | End-user docs (`noix-docs`, branch `pulsix`): reconcile with what v0.1 actually ships | planned | the branch describes the v1 target |

## v0.2 · Inventory & Ansible

Three plans cover this milestone. **v0.2a Inventory**
(`docs/superpowers/plans/2026-09-11-v0.2a-inventory.md`) covers 2.1–2.7a and the
key-generation half of 2.7b (visible public key + snippets) — shipped. **v0.2b
Execution** (`docs/superpowers/plans/2026-09-14-v0.2b-execution.md`) covers the rest
of 2.7b (now 2.7c: bootstrap, connection test, host-key pinning), 2.8–2.12a and 2.14 —
shipped. **v0.2c Automation** (`docs/superpowers/plans/2026-09-18-v0.2c-automation.md`)
covers 2.13, 2.13b, 2.23, 2.24 and the push webhook of 2.8 — shipped. **v0.2d**
covers 2.13a and 2.13c (notification channels, maintenance windows, schedule
notifications, the calendar); design: `docs/superpowers/specs/2026-09-18-v0.2c-automation-design.md`.

| # | Item | Status | Notes |
|---|------|--------|-------|
| 2.1 | Inventory groups: hierarchy, parents/children | done | |
| 2.2 | Variable schema builder per group: string, int, float, bool, enum, secret, list, repeater, sub-schema | done | JSON schema stored per group |
| 2.3 | Scope flag per field: `group` (set once) or `host` (asked on assignment) | done | |
| 2.4 | Host-to-group assignment form generated from schema, validation | done | |
| 2.5 | Host override of group values (optional per field) | done | |
| 2.6 | Inventory export to Ansible (dynamic inventory JSON + group_vars/host_vars) | done | masked dynamic-inventory preview done (v0.2a); the worker's revealed snapshot (secrets included, host-key-mismatch hosts excluded) rides the job claim response (v0.2b) |
| 2.7 | Credentials & secrets page: SSH keys/passwords, Git deploy keys/tokens, channel secrets in one encrypted store with usage, expiry warnings and key rotation via playbook | done | store, usage, expiry done (v0.2a); rotation (v0.2b): a rotate_key job per host/jump host, a verification connection test per host that waits for its host's rotation (job dependency), the new key sealed only when everything verified, then a revoke_key job per target removes the previous key; one rotation per credential at a time (2.28) |
| 2.7a | SSH jump hosts (bastions): org-level objects, chainable, inherited zone → group → host, rendered as ProxyJump | done | |
| 2.7b | SSH onboarding: Pulsix-generated or imported keys with visible public key + snippets (authorized_keys, cloud-init, Terraform) | done | mockup: enrollment step 3 |
| 2.7c | Bootstrap playbook, connection test from the worker, host-key TOFU + pinning, sudo/Python checks, daily re-test | done | connection test is Go (`golang.org/x/crypto/ssh`), not ansible; a pin is fingerprint + full key and is enforced with strict host key checking on every playbook connection (2.27); a changed key blocks SSH-bound jobs until a test confirms a pinned key again; daily re-test is a staggered scheduler goroutine, also part of every rotation; bootstrap materializes an embedded playbook with a one-time, never-stored login |
| 2.8 | Several Git repos per org (deploy key / token, branch, subpath), pull before run, `requirements.yml` install, push webhook for auto-sync, sync errors surfaced | done | sync runs as its own job kind (`sync_repo`) on the worker before every run; push-webhook auto-sync through a `repo_sync` webhook trigger (`POST /api/v1/hooks/repos/{id}`, v0.2c) |
| 2.9 | Ad-hoc playbook editor in GUI | done | validated at save time (parses as YAML, first element a mapping with `hosts`); full syntax check happens on the worker before a run |
| 2.10 | Job templates: playbook, inventory scope, limit, extra vars, credentials | done | extra vars validated against an optional schema; zone selection derived from targeted hosts, not stored; no schedule field yet (2.13) |
| 2.11 | Worker: registers to manager, pulls jobs from Postgres queue, runs ansible-runner | done | enrollment is a manager CLI command (`pulsix-manager worker add`) issuing a client certificate from the instance CA, not a UI token flow; `SELECT … FOR UPDATE SKIP LOCKED` claim; a worker claims only for the zone (and organization) its certificate was enrolled for, and only the claiming worker may report the job's result (2.25); the compose stack runs a separate `worker` container enrolled by a one-shot `worker-enroll` service (no database or master-key access in the worker); the in-process embedded worker (`PULSIX_EMBEDDED_WORKER=1`) is opt-in for trusted single-tenant setups only; v0.2c adds token enrollment over the API and the Workers page (2.23) |
| 2.12 | Run view: live log stream, per-host result, retry failed hosts | done | SSE stream with poll fallback on gaps/errors; cancel also shipped (not separately tracked) |
| 2.12a | E2E: playbook from Git fixture against SSH container via bastion, schema → inventory golden file | done | milestone gate: `internal/e2e` (Go harness, fake and real-ansible-runner modes; both pass locally as of the final review fix wave, the real mode against ansible-core 2.21.4 + ansible-runner 2.4.3 in a venv; `PULSIX_E2E_REAL_ANSIBLE=1` fails when the binaries are missing); the scenario includes a host key change after pinning. CI has not run the branch yet (pushing is the owner's call). The Playwright UI half (`e2e-ansible-ui`) stays commented — see `.github/workflows/ci.yml` |
| 2.13 | Schedules for job templates: cron or simple mode, timezone, guards (overlap, maintenance, catch-up), pause | done | v0.2c: own DST-correct cron evaluator (`internal/cronexpr`), simple mode, time zones, guards overlap and catch-up, overrides with sealed secrets, run history; the maintenance guard follows with maintenance windows (v0.2d) |
| 2.13a | Schedule notifications: rules per schedule (failure / change / success / skipped → channels), weekly digest; every run in the activity feed | planned | v0.2d |
| 2.13b | Incoming webhook trigger per job template: URL + HMAC or token, IP allowlist, payload → extra vars mapping, rate limit | done | v0.2c: presets Pulsix, GitHub, Gitea, GitLab and bearer; timestamp window; allowlist behind `PULSIX_TRUSTED_PROXIES`; RFC 9535 JSONPath subset mapping; token bucket shared across managers plus a per-address limit; `on_active`; test call; delivery log. Also serves repository sync (2.8) |
| 2.13c | Calendar view of scheduled runs and maintenance windows | planned | v0.2d |
| 2.23 | Workers page: list enrolled workers per zone (name, zone, last seen, version) and revoke one from the interface, with an API behind it | done | v0.2c: Workers page and API; one-time enrollment tokens with docker and compose snippets; 90-day certificates the worker renews itself; `pulsix-manager worker add` stays for scripted setups |
| 2.24 | Revoke a worker certificate: revoked serials refused even if a registry row is recreated, bundle rotation for the compose worker | done | v0.2c: revocation marks the worker and every serial in `worker_certificates` revoked, ends its streams and requeues its jobs; the compose worker's bundle volume is writable so renewed certificates persist |
| 2.25 | Bind `ReportResult` to the claiming worker | done | v0.2b review fix: the claim records `jobs.worker_id`; `FinishFor` acts only on a job of the reporting worker's organization and zone whose `worker_id` is that worker, else `NotFound` without touching the row (checked before the finish transaction and again in its UPDATE) |
| 2.14 | Ad-hoc command on hosts (`ansible -m shell`) | done | `shell` module only in v0.2b; enqueued as its own job kind, runs and streams like any other job |

## v0.2a follow-ups for v0.2b

Items the v0.2a final whole-branch review deferred to the v0.2b plan. Recorded 2026-09-11.

| # | Item | Status | Notes |
|---|------|--------|-------|
| 2.15 | Serialize tree-changing group writes and chain-changing jump-host writes per organization (transaction + advisory lock), closing the concurrent-move cycle and the jump-host chain race | done | `tree_write.sql` named in the task brief does not exist; the lock lives in each package's `SQLStore` (`WithTreeLock`/`WithChainLock`), taken via `db.DB.WithOrg`. Locked: group create (with a parent)/update/delete, bastion create (with a via)/update/delete. Concurrency tests race many pairs at once through a shared start barrier — a single pair does not reproduce the corruption reliably |
| 2.16 | Render inventories inside one repeatable-read transaction | done | `db.DB.WithReadTx` opens one REPEATABLE READ, READ ONLY transaction and binds it into `ctx`; `Exec`/`Query`/`QueryRow`/`Begin` all honor a bound transaction ahead of the usual per-statement path, so every domain store the renderer reads through — regardless of package — shares one transaction with no changes needed outside `internal/db` and `internal/inventory` |
| 2.17 | Credential metadata reads without the sealed secret column; a separate secret read for the worker | done | the worker's separate read (`GetCredentialSecret`/`LoadSecret`) already existed from an earlier task; `GetCredential` and `ListCredentials` now name every column but `secret` explicitly, closing the metadata side |
| 2.18 | Host SSH card read-only for viewers | done | the brief assumed a permission list on the SPA session; the session carries only the org role (`web/src/lib/roles.ts`), so this gates on `may(role, 'inventory.write')`, the same way `HostDetailPage`/`ConnectionTestCard` already do. No separate "test" button exists in this card to hide |
| 2.19 | Cross-org and malformed-id API tests for every inventory write route | done | extended to credential and sshconf write routes too, per the task's actual scope; `inventory_member_handlers_test.go` did not exist and was created. Every route already behaved correctly (404/400, never 500) — these tests lock that in |
| 2.20 | Assign-host picker: stop auto-loading further pages after a failed page load and show the error; show an error state when the first page fails | done | the auto-load effect and the "every host already a member" state now also check `isFetchNextPageError`; two banners (first-page failure, later-page failure with a retry button) added |
| 2.21 | Group and membership reads mask every non-empty value of a secret field, not only sealed ones | done | `schema.MaskDeclared(fields, values)`; `internal/templates` had already hand-rolled the same fix independently (its own comment credited "backlog 2.21's direction") and now delegates to the shared function instead |
| 2.22 | Snapshot: clearer message when a container field changed type above a secret sub-field; decide whether a stray sealed value outside the job's scope should fail a scoped snapshot | done | message part: `schema.StraySealed` now reports each stray as a leaf or a container finding, and the renderer's error text differs accordingly, naming the container path. Scope decision (recorded in the v0.2b plan): fail closed stays; a stray outside the requested scope still fails the whole snapshot, since secrets are resolved for the whole organization before `render.Render` narrows to scope — documented in code, no behavior change |

## v0.2b follow-ups from the final branch review

Findings of the v0.2b final whole-branch review (2026-09-16) and their fix wave.
Shipped items first, then what was deliberately deferred.

| # | Item | Status | Notes |
|---|------|--------|-------|
| 2.26 | A run claim renders exactly the job's hosts, and re-checks changed host keys at claim | done | the template scope is resolved and split per zone at enqueue; the template `limit` still applies on top, per zone job |
| 2.27 | Host-key pins enforced on the connections that run playbooks | done | full host key stored with the pin (migration 00021); claims ship `known_hosts`; the runner uses a per-host `HostKeyAlias` and `StrictHostKeyChecking=yes`; ad-hoc, bootstrap and rotation refuse changed keys (409); no direct-dial preflight |
| 2.28 | Key rotation revokes the old key, orders verification after installation, one rotation per credential | done | `jobs.depends_on`, `jobs.rotation_credential_id` (migration 00022), `revoke_key` job kind and embedded playbook |
| 2.29 | Every terminal transition wipes the sealed payload and runs the finish sinks | done | payload-build failure, reaper failures, worker finish share one helper; a cancel wipes only when the job became terminal |
| 2.30 | Explicit gRPC message limits, event payload bound, claim attempts limit | done | 64 MiB claims / 16 MiB worker messages / 1 MiB per event; `PULSIX_JOB_MAX_ATTEMPTS` (default 3, migration 00020) |
| 2.31 | Run events redacted on the worker; each ansible event stored once | done | claim `redact` list (credentials, secret extra vars, secret inventory vars, one-time logins); threat model in spec §14 |
| 2.32 | Run event seq monotonic across attempts, events stamped with their attempt | done | the run view groups attempts |
| 2.33 | The real-ansible gate runs | done | found and fixed: ansible-runner `-b` dropped the playbook; errored runs reported no reason |
| 2.34 | Secret-typed template extra vars sealed at rest | done | keep/masked markers keep the stored value; the SPA sends keep markers |
| 2.35 | Smaller review items | done | generic SSE error frames; retest lock released on cancelled context; `job.result` audit carries the org; 00017 down order; `jobs (org_id, zone_id)` foreign key (migration 00023); git `--` separators and no leading `-` in repo URL/branch; rotate_key claim no longer ships the pending private key; ansible exit code 4 maps to failed |
| 2.36 | Template extra vars and claims cannot weaken host-key enforcement | done | re-review N1: connection-security vars refused (422) and forced by the worker as extra vars |
| 2.40 | Pin jump-host (bastion) host keys | planned | today a jump host's key is trusted on first use per job; a pinned target is still verified end to end through it |
| 2.41 | Pin Git server host keys | planned | repo fetches accept a new server key per job (`accept-new` in the run's home) |
| 2.42 | Run playbooks under a separate uid from the worker process | planned | hardening: a `localhost` task can read the worker's client key today (documented trust model, spec §14); needs a setuid helper or CAP_SETUID in the worker image |
| 2.43 | Show "pin not enforceable" for fingerprint-only pins in the API/UI | planned | pins made before migration 00021 have no full key; claims for such hosts fail with a message asking for a connection test, and the next confirming test completes the pin (the daily re-test does it within a day). Documented, not yet visible as a state |
| 2.44 | Option to refuse jobs against unpinned hosts | planned | decision for now: unpinned hosts are trusted on first use for one job only (pinning happens on the connection test, spec §5) |
| 2.45 | Evaluate the template `limit` per zone at enqueue | planned | a limit that matches no host of one zone makes that zone's job error in ansible |
| 2.46 | Revocation targets snapshot | planned | sign-off 3 (accepted for v0.2b): revocations go to the hosts and jump hosts referencing the credential when the rotation verified; a target that stopped referencing it in between keeps the old key |
| 2.49 | Events of an old attempt's still-open stream are stamped with the new attempt and keep its binding alive | planned | re-review N2: carry the bound token or attempt from the stream into the append |
| 2.50 | Event append scans every hypertable chunk for max(seq) | planned | re-review N3: bound on the job's created_at or keep a per-job counter on jobs |
| 2.51 | Event payload truncation's UTF-8 back-off is quadratic and can empty the head | planned | re-review N4: back off at most 3 bytes or use strings.ToValidUTF8 |
| 2.52 | Templates with legacy plaintext secret extra vars cannot be saved with keep markers | planned | re-review N5: accept a plaintext stored value under a secret field as keepable and seal it (makes 2.47 hold) |
| 2.53 | Reaper exhaustion does not re-check staleness at the transition | planned | re-review N6: add the staleness predicate to the transition or re-check stream liveness before failing |
| 2.47 | Re-seal legacy plaintext template secrets without a save | planned | values stored before sealing are sealed on the next save of the template |
| 2.48 | Redaction limits | planned | values shorter than 4 characters and transformed secrets (base64, hashes) are not recognized; `no_log` stays the playbook author's tool |

## v0.2c follow-ups

Decisions and limits recorded while planning v0.2c (2026-09-18).

| # | Item | Status | Notes |
|---|------|--------|-------|
| 2.54 | A revocation cuts open event streams on every manager | planned | v0.6: today only the manager that served the revocation cuts the worker's streams; every manager refuses its next RPC |
| 2.55 | Retry-failed of a triggered run keeps the run's overrides | idea | overrides travel sealed in the job payload, which is wiped when the job ends; a retry uses the template's own limit and extra vars (documented) |
| 2.56 | Delete job templates from the SPA | planned | the API can delete a template, which cascades to its schedules and webhook triggers; the SPA has no delete action yet |
| 2.57 | Stop a revoked worker's running ansible process | idea | the manager cannot reach into the worker; the revoked worker's results are refused and the job is requeued elsewhere; the claim query itself does not check worker status, so an in-flight claim already handed to a worker can still finish and be accepted until the reaper's stale window catches it |
| 2.58 | Per-address limits shared across managers | idea | the hook and enrollment per-address limits count per manager; the per-trigger token bucket is already shared |
| 2.59 | Five pre-existing gofmt deviations from v0.2b escape CI lint | planned | found during the v0.2c final review: `internal/api/hostops_handlers.go`, `internal/api/template_handlers.go` (+ `_test.go`), `internal/jobs/system_claim_test.go`, `internal/templates/service_test.go`; `golangci-lint run` is green on all of them, so the CI lint gate needs its own `gofmt -l` (or `gofumpt`) check before these can be trusted to stay fixed |
| 2.60 | Schedule ticker robustness under connection loss and shutdown | idea | v0.2c ticker deferrals: a decide-before-savepoint ordering re-fires enqueued rows on mid-batch connection loss; a shutdown mid-enqueue records the occurrence `failed` though it was actually retried; `ProcessDue` returning early on the batch deadline makes `Tick` loop immediately instead of waiting for the next interval; the outer commit/rollback calls run on an unbounded `WithoutCancel` context; the cronexpr occurrence cap has an untested edge case |
| 2.61 | Schedule service edge cases | idea | v0.2c deferrals: `RunNow` inserts a `schedule_runs` row without the ticker's retention trim; resuming a non-paused schedule can recompute `next_run_at` past a run that was due; `RunNow` enqueuing successfully but `RecordRun` failing reports an error to the operator although a retry would double-run; `Update` computes `next_run_at` before override validation and the row lock wait, which can interleave with the ticker; `Service.Runs` does not clamp its `limit` (relies on the API's clamp); `List` runs `DISTINCT ON` over every schedule run of the org on each call |
| 2.62 | Webhook and enrollment timing/ordering hardening | idea | v0.2c deferrals: an unknown trigger path skips the HMAC compute, so response timing can reveal whether a trigger id exists; the same is true of an unknown enrollment token id; the per-address webhook rate limiter's effectiveness against distributed sources is unverified; the rate-limit bucket's time handling has an untested edge; JSONPath wildcard `[*]` over a JSON object returns keys in map-iteration (random) order |
| 2.63 | `worker_enrollments` has no foreign key to `zones`/`organizations` | planned | needs a new migration; referential integrity for enrollment rows currently relies on application logic only |
| 2.64 | Worker certificate and identity hardening | idea | v0.2c deferrals: a renewal CSR is not cryptographically bound to the worker's current key (undocumented on the server side); the gRPC server's certificate re-issue retries on every handshake without backoff and logs through the global logger; the worker does not verify the full chain of a returned leaf; the identity directory's permissions and fsync behaviour could be tightened; `writeFileAtomic` is duplicated instead of shared; a legacy hand-made `embedded` row in the default zone is not detected as reserved |
| 2.65 | Revocation response and audit gaps | idea | v0.2c deferrals: `POST /workers/{id}/revoke` answers 500 without an audit entry when job requeueing fails after the revoke commit; the revoke API response's worker object has an empty `zone_slug` (the schema requires one) |
| 2.66 | `PULSIX_GRPC_PUBLIC_ADDR` validation gaps | planned | v0.2c: a validation error names the wrong variable, and a bare `:443` (no host) is accepted |

## v0.3 · Agent & monitoring

| # | Item | Status | Notes |
|---|------|--------|-------|
| 3.1 | Agent binary: Linux amd64/arm64 (systemd) | planned | Go, single static binary |
| 3.2 | Agent binary: Windows service | planned | |
| 3.3 | Agent binary: macOS launchd | planned | |
| 3.4 | Enrollment: one-time token, install script, mTLS cert issued by manager CA | planned | outbound-only |
| 3.4a | E2E: agent enrollment, monitoring pipeline (threshold → alert → notify → resolve), remote checks | planned | milestone gate |
| 3.5 | Agent local config `agent.yaml`: secrets by name, static local checks | planned | secrets never sent to manager |
| 3.6 | Check definitions pushed from GUI to agent, secret references by name | planned | hybrid model |
| 3.6a | Check engine: plugins in code with declared parameter schema | planned | cpu, memory, disk, service, process, smart, docker, db, web, http, tcp, icmp, tls, dns, snmp, script |
| 3.6b | Check definitions as data: plugin + params + interval + binding (host / monitoring group / org) | planned | seeded default set ("Linux base", "PostgreSQL", ...) editable in GUI |
| 3.6c | Discovery of items (mounts, services, interfaces, containers) with per-item overrides | planned | new items inherit rules |
| 3.6d | Rule engine: metric, operator, value, duration → state; inheritance plugin → org → group → host → item | planned | "effective config" view shows source of each value |
| 3.6e | Rule editor: form mode + expression mode (same rule model) | planned | e.g. `avg(cpu.load5, 10m) > cores * 0.8` |
| 3.6f | Hysteresis / flap protection, unknown state on missing data | planned | |
| 3.6g | Custom script checks: Nagios plugin format (exit code + perfdata) and JSON format | planned | reuse existing check_* plugins |
| 3.6h | HTTP check with JSONPath extraction, SNMP OID checks | planned | |
| 3.7 | System checks: CPU, load, memory, swap, disk usage, inodes, network, uptime | planned | |
| 3.8 | Service checks: systemd / Windows services / launchd | planned | |
| 3.9 | Hardware: SMART, temperatures, RAID (mdadm) | planned | |
| 3.10 | Database checks: PostgreSQL, MySQL/MariaDB, Redis | planned | |
| 3.11 | Web server checks: nginx, Apache, Caddy status endpoints, PHP-FPM | planned | |
| 3.12 | Container checks: Docker containers and their state | planned | |
| 3.13 | Synthetic checks: ICMP, TCP (with banner match), HTTP(S), TLS, DNS, SNMP | planned | for hosts with or without agent |
| 3.13a | Probes per synthetic check: manager, zone workers, agent groups as vantage points; per-probe results and metrics; fail policy any/majority/all with second-probe confirmation | planned | probe ships in every agent binary, no extra install |
| 3.13b | Web assertions: status, response time with DNS/TCP/TLS/TTFB/transfer breakdown, body contains and not contains, regex, HTML selector, JSON path, header, redirect count, content size, TLS expiry and issuer; severity per assertion; failed assertions shown on the alert | planned | |
| 3.13c | Agent config pull every 60 s in addition to push, config version per agent shown in the GUI | planned | a missed push heals itself |
| 3.14 | Time series ingest (gRPC stream), TimescaleDB hypertables, downsampling | planned | |
| 3.15 | Retention per org (30/90/365 d) | planned | |
| 3.16 | Graphs per host and per check (uPlot), fleet overview | planned | |
| 3.17 | Facts collection (OS, kernel, CPU, memory, IPs), auto tags | planned | |
| 3.18 | Agent auto-update from manager | idea | |

## v0.4 · Alerting

| # | Item | Status | Notes |
|---|------|--------|-------|
| 4.1 | Monitoring groups: tag selector (`role=db AND env=prod`) + manual members + exclusions | planned | separate from inventory groups |
| 4.1a | Nested monitoring groups: optional parent, membership mode within-parent or independent, priority; hosts stay many-to-many | planned | |
| 4.1b | Inheritance semantics: rules override (deepest wins, priority breaks ties, ambiguous ties flagged), check templates / routes / maintenance add up; effective-rules and route-union view per host | planned | |
| 4.2 | Check templates attached to monitoring groups | planned | |
| 4.3 | Alert rules: thresholds, duration, severity, per host / group | planned | |
| 4.4 | Notification schedules: time windows, weekdays, on-call rotation | planned | |
| 4.5 | Channels: email (SMTP) | planned | |
| 4.6 | Channels: generic webhook with template | planned | covers n8n, Discord, ... |
| 4.7 | Channels: Telegram, Slack, Mattermost native | planned | |
| 4.8 | Channels: ntfy, Pushover, Twilio SMS, PagerDuty | planned | |
| 4.9 | Escalation chains (after N min unacknowledged → next channel) | planned | |
| 4.10 | Acknowledge, snooze, silence | planned | |
| 4.11 | Maintenance windows on host / group / org + central list, visibility internal or public on selected status pages | planned | |
| 4.12 | Alert history and timeline per host | planned | |

## v0.5 · Status pages

| # | Item | Status | Notes |
|---|------|--------|-------|
| 5.1 | Status pages per org, public URL `<slug>.status.pulsix.dev` | planned | |
| 5.2 | Custom domain per page | planned | |
| 5.3 | Components: hosts, monitoring groups, single checks, with display names | planned | never expose hostnames or metrics |
| 5.4 | Enable "show on status page X" per host / monitoring group | planned | |
| 5.5 | 90-day availability bars, current state, uptime percentage | planned | |
| 5.6 | Incidents: org-level object published to one or more pages; created from page admin, from an alert, or from host/group page | planned | |
| 5.7 | Scheduled maintenance announcements (linked to maintenance windows) | planned | |
| 5.8 | Visitor email subscriptions | planned | |
| 5.9 | Status page theming (logo, colours) | idea | |

## v0.6 · Scale & HA

| # | Item | Status | Notes |
|---|------|--------|-------|
| 6.1 | Manager stateless, multiple instances behind load balancer | planned | |
| 6.2 | Workers per zone, agents connect to zone worker, worker relays to manager | planned | |
| 6.3 | Horizontal worker scaling (queue in Postgres, SKIP LOCKED) | planned | |
| 6.4 | Worker health, agent reassignment when a worker goes down | planned | |
| 6.4a | E2E: manager takeover, worker failure, agent reconnect; nightly load test with 1 000 simulated agents | planned | milestone gate |
| 6.5 | Postgres HA guidance (external managed DB, Patroni) | idea | docs, not code |

## v1.0 · Release

| # | Item | Status | Notes |
|---|------|--------|-------|
| 7.1 | Security review, OWASP audit | planned | |
| 7.2 | Backup and restore | planned | |
| 7.3 | Upgrade path and migrations policy | planned | |
| 7.4 | Documentation site | planned | |
| 7.5 | Docker images on Docker Hub (noixdev/pulsix-*) and Harbor, agent binaries on GitHub releases, all from one `vX.Y.Z` tag | planned | single version for manager, worker, agent |
| 7.6 | Make repo public, landing page on noix.dev | planned | repo is private until then; no tech stack in public copy |

## Decisions (short)

- Stack: Go (manager, worker, agent), React/TypeScript SPA, PostgreSQL + TimescaleDB, gRPC over mTLS, no broker.
- Multi-tenant and self-hostable. Manager HA, workers per zone.
- Host capabilities `managed` (Ansible via SSH) and `monitored` (agent and/or remote checks) are independent.
- Inventory groups (Ansible, schema builder) and monitoring groups (tag selectors, alerting) are separate concepts.
- Agent config is hybrid: checks from GUI, secrets local only.
- One version and one tag for manager, worker and agent; manager accepts same and previous minor version.
- Check engine layered like Checkmk/Zabbix: plugins in code, definitions and rules as data, inheritance plugin → org → group → host → item, discovery, custom checks in Nagios or JSON format.
- Status colours (ok/warn/crit/unknown) are reserved for states; cyan is the action colour.
- GitHub repo `NoiXdev/pulsix` starts **private**; goes public with the v1.0 launch.
- Test policy: every feature ships with tests; E2E scenarios (agent, monitoring, Ansible, HA, auth, tenancy, status pages) are milestone gates; coverage enforced in CI.
