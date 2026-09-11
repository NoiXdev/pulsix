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
key-generation half of 2.7b (visible public key + snippets). **v0.2b Execution** covers
the rest of 2.7b (now 2.7c: bootstrap, connection test, host-key pinning), 2.8–2.12a.
**v0.2c Automation** covers 2.13–2.14.

| # | Item | Status | Notes |
|---|------|--------|-------|
| 2.1 | Inventory groups: hierarchy, parents/children | done | |
| 2.2 | Variable schema builder per group: string, int, float, bool, enum, secret, list, repeater, sub-schema | done | JSON schema stored per group |
| 2.3 | Scope flag per field: `group` (set once) or `host` (asked on assignment) | done | |
| 2.4 | Host-to-group assignment form generated from schema, validation | done | |
| 2.5 | Host override of group values (optional per field) | done | |
| 2.6 | Inventory export to Ansible (dynamic inventory JSON + group_vars/host_vars) | in progress | masked dynamic-inventory preview done (v0.2a); the worker's revealed snapshot and its delivery arrive with v0.2b |
| 2.7 | Credentials & secrets page: SSH keys/passwords, Git deploy keys/tokens, channel secrets in one encrypted store with usage, expiry warnings and key rotation via playbook | in progress | store, usage, expiry done (v0.2a); rotation via playbook in v0.2b |
| 2.7a | SSH jump hosts (bastions): org-level objects, chainable, inherited zone → group → host, rendered as ProxyJump | done | |
| 2.7b | SSH onboarding: Pulsix-generated or imported keys with visible public key + snippets (authorized_keys, cloud-init, Terraform) | done | mockup: enrollment step 3 |
| 2.7c | Bootstrap playbook, connection test from the worker, host-key TOFU + pinning, sudo/Python checks, daily re-test | planned | v0.2b |
| 2.8 | Several Git repos per org (deploy key / token, branch, subpath), pull before run, `requirements.yml` install, push webhook for auto-sync, sync errors surfaced | planned | main path for playbooks |
| 2.9 | Ad-hoc playbook editor in GUI | planned | small playbooks, stored in DB |
| 2.10 | Job templates: playbook, inventory scope, limit, extra vars, credentials | planned | |
| 2.11 | Worker: registers to manager, pulls jobs from Postgres queue, runs ansible-runner | planned | |
| 2.12 | Run view: live log stream, per-host result, retry failed hosts | planned | |
| 2.12a | E2E: playbook from Git fixture against SSH container via bastion, schema → inventory golden file | planned | milestone gate |
| 2.13 | Schedules for job templates: cron or simple mode, timezone, guards (overlap, maintenance, catch-up), pause | planned | |
| 2.13a | Schedule notifications: rules per schedule (failure / change / success / skipped → channels), weekly digest; every run in the activity feed | planned | |
| 2.13b | Incoming webhook trigger per job template: URL + HMAC or token, IP allowlist, payload → extra vars mapping, rate limit | planned | |
| 2.13c | Calendar view of scheduled runs and maintenance windows | planned | |
| 2.14 | Ad-hoc command on hosts (`ansible -m shell`) | idea | |

## v0.2a follow-ups for v0.2b

Items the v0.2a final whole-branch review deferred to the v0.2b plan. Recorded 2026-09-11.

| # | Item | Status | Notes |
|---|------|--------|-------|
| 2.15 | Serialize tree-changing group writes and chain-changing jump-host writes per organization (transaction + advisory lock), closing the concurrent-move cycle and the jump-host chain race | planned | until then the renderer fails closed on a group cycle or a broken chain (409 on the previews) |
| 2.16 | Render inventories inside one repeatable-read transaction | planned | today the renderer's reads (groups, memberships, hosts, SSH settings, jump hosts, credentials) are separate statements |
| 2.17 | Credential metadata reads without the sealed secret column; a separate secret read for the worker | planned | |
| 2.18 | Host SSH card read-only for viewers | planned | |
| 2.19 | Cross-org and malformed-id API tests for every inventory write route | planned | |
| 2.20 | Assign-host picker: stop auto-loading further pages after a failed page load and show the error; show an error state when the first page fails | planned | a persistent error on page 2+ while every loaded host is already a member refetches about once a second |
| 2.21 | Group and membership reads mask every non-empty value of a secret field, not only sealed ones | planned | after a string → secret schema change the old plain value is shown until the next save seals it |
| 2.22 | Snapshot: clearer message when a container field changed type above a secret sub-field; decide whether a stray sealed value outside the job's scope should fail a scoped snapshot | planned | decide in the v0.2b plan; the snapshot fails closed today |

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
