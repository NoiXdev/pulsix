# Pulsix — architecture design

Status: draft for review · Date: 2026-09-10 · Owner: NoiXdev

Pulsix is a multi-tenant, self-hostable platform for server management (Ansible) and
monitoring (agents + remote checks) with alerting and public status pages. This document
is the design baseline for the implementation plan. The feature backlog with milestones
lives in `docs/BACKLOG.md`; brand assets in `brand/`.

## 1. Goals and non-goals

Goals

- One installation serves many organizations (tenants) with hard data separation; the
  same build runs as a single-node self-hosted `docker compose up`.
- Ansible is the management engine. The full inventory (hosts, groups, variables) is
  authored in the GUI and exported to Ansible at run time.
- Per inventory group, a typed variable schema (string, int, float, bool, enum, secret,
  list, repeater, sub-schema) with a scope flag `group` or `host`; host-scoped fields are
  asked when a host is assigned to the group.
- Monitoring of services, load, hardware, disks, databases, web servers, containers and
  network endpoints through a local agent and worker-side remote checks; time series with
  graphs and per-org retention.
- Check engine with plugins in code, check definitions and rules as data, inheritance and
  discovery (Checkmk/Zabbix model), custom script checks.
- Alerting with monitoring groups, schedules, channels, escalation and maintenance windows.
- Public status pages with incidents, maintenance and visitor subscriptions.
- Passkeys and TOTP MFA; complete REST API with an explorer; the SPA uses only that API.
- Encrypted, authenticated communication for agents and workers with one-line enrollment.
- Manager high availability, workers per zone, horizontal scaling.

Non-goals (v1)

- Replacing Ansible with an own configuration language.
- Kubernetes-native operator or Helm chart (Compose first; Helm may follow).
- Log management (only check output and job logs are stored).
- Built-in on-call scheduling beyond simple rotations (PagerDuty integration covers that).

## 2. System overview

```
                 ┌──────────────────────── manager (stateless, N instances) ────────────────────────┐
  browser ──HTTPS─▶ REST API + SPA + status pages + OpenAPI  │  scheduler  │  alert engine  │  CA  │
                 └──────────────┬───────────────────────────────────┬──────────────────────────────┘
                                │ SQL                               │ gRPC/mTLS (worker → manager, outbound)
                     ┌──────────▼──────────┐               ┌────────▼────────┐   ┌────────────────┐
                     │ PostgreSQL          │               │ worker  zone=fra│   │ worker zone=hel│
                     │  + TimescaleDB      │               │ ansible-runner  │   │ ansible-runner │
                     │ (config, jobs,      │               │ remote checks   │   │ remote checks  │
                     │  metrics, states)   │               │ agent gateway   │   │ agent gateway  │
                     └─────────────────────┘               └───▲─────▲───────┘   └───▲────────────┘
                                                               │ gRPC/mTLS (agent → worker, outbound)
                                                    ┌──────────┴──┐  ┌───┴──────┐   ┌───┴──────┐
                                                    │ agent web-01│  │ agent db │   │ agent pi │
                                                    └─────────────┘  └──────────┘   └──────────┘
```

Three binaries, one language, one database:

| Component | Language | Role |
|-----------|----------|------|
| `pulsix-manager` | Go | REST API, SPA hosting, OpenAPI, auth, scheduler, alert engine, status pages, internal CA, gRPC endpoint for workers |
| `pulsix-worker` | Go (+ Python ansible-runner in the image) | Runs Ansible jobs, executes remote checks, terminates agent connections for its zone, relays metrics to the manager |
| `pulsix-agent` | Go | Collects metrics and runs checks on a host, outbound-only connection to a worker |
| `web/` | TypeScript, React, Vite | SPA, talks to the public REST API only |

All connections are outbound from the more exposed side: agent → worker, worker → manager.
No inbound port on hosts, no inbound port on workers. The manager exposes HTTPS (browser,
API, status pages) and one gRPC port for workers.

Playbooks never run in a process or container that holds the master key or database
credentials. The single-node Compose setup therefore runs a separate `worker` container
for the first organization's `default` zone, enrolled automatically by a one-shot
`worker-enroll` service (the manager image's `worker add --bundle-dir --wait`), which
writes the certificate bundle into a volume the worker mounts read-only; the worker
container itself has neither database access nor the master key. An embedded in-process
worker (`PULSIX_EMBEDDED_WORKER`) exists as an opt-in for trusted single-tenant setups
only; it is off by default, and the minimal manager image carries no Ansible runtime, so
it runs connection tests only there. (Revised in the v0.2b review.)

## 3. Tenancy and access

- `organization` is the tenant. Every domain table carries `org_id`; the API scopes
  every query by the caller's org. Row-level security policies in Postgres back this up.
- Users belong to one or more orgs with a role per org: `owner`, `admin`, `operator`,
  `viewer`. Role permissions are a fixed matrix (no custom roles in v1).
- Authentication: email + password with mandatory TOTP, or passkey (WebAuthn, resident
  key). Recovery codes. Sessions are HTTP-only cookies for the SPA; API tokens (scoped to
  org and role, optional expiry) for automation. Passkeys via `go-webauthn`.
- Instance administrator: a separate `instance_admin` flag manages orgs, instance settings
  and licensing-free feature flags. Not visible inside org data.
- Audit log per org for every mutating API call (who, what, before/after summary).

## 4. Data model (core)

Relational, PostgreSQL 18 with the TimescaleDB extension. Only the important tables;
naming is `snake_case`, IDs are UUIDv7.

Tenancy and auth: `organizations`, `users`, `org_memberships(role)`, `passkeys`,
`totp_secrets`, `api_tokens`, `audit_events`.

Topology: `zones(org_id, slug, name)`, `workers(zone_id, name, status active|revoked,
revoked_at, last_seen, version, capabilities)`, `worker_certificates(worker_id, serial,
not_before, not_after, revoked_at)`, `worker_enrollments(zone_id, name, secret_hash,
expires_at, used_at)`, `hosts(org_id, zone_id, name, address, managed bool, monitored
bool, facts jsonb, last_seen)`, `host_tags(host_id, key, value, source manual|fact)`.

Inventory (Ansible): `inventory_groups(org_id, parent_id, name, schema jsonb, vars jsonb,
bastion_id, revision)`, `inventory_memberships(group_id, host_id, org_id, vars jsonb,
revision)` — the column is `vars`, not `values`, because `VALUES` is an SQL keyword; the
API and this spec keep the word "values" for the concept. `revision` backs the `If-Match`
concurrency check (§15) on group and membership writes. `credentials(org_id, name, kind
ssh_key|ssh_password|git_key|git_token, username, public_key, fingerprint, key_type,
sealed secret, expires_at)` replaces `ssh_credentials`: one encrypted store for SSH keys
and passwords, Git deploy keys and tokens, with `public_key`/`fingerprint`/`key_type` set
only for `ssh_key`/`git_key`. `bastions(org_id, name, address, port, username,
credential_id, via_bastion_id)` (SSH jump hosts, chainable up to four hops, cycles
rejected on write). `host_ssh(host_id, org_id, username, port, credential_id, bastion_mode
inherit|none|explicit, bastion_id)`, one row per host with settings, defaulting to
inherit when absent; `zones` also carry a default `bastion_id`.

Ansible content and jobs: `git_repos(org_id, url, credential, branch, last_sync)`,
`playbooks(org_id, repo_id|null, path|inline, source repo|editor)`, `job_templates(org_id,
playbook_id, limit, extra_vars, credential_id)`, `jobs(template_id, status, zone_id,
worker_id, trigger manual|schedule|webhook|system, trigger_ref, started, finished, stats
jsonb)`, `schedules(template_id, mode cron|simple, cron, simple jsonb, timezone, guards
jsonb, limit, extra_vars, paused, next_run_at)`, `schedule_runs(schedule_id, due_at,
outcome, reason, job_group_id)`, `job_triggers(kind template_run|repo_sync,
template_id|repo_id, preset, auth hmac|token, secret, allow_cidrs, mapping jsonb,
on_active, rate_limit jsonb)`, `trigger_deliveries(trigger_id, at, source_ip, outcome,
reason, job_group_id)`, `job_events(job_id, seq, host,
task, payload jsonb)` (hypertable, compressed after 7 d).

Monitoring: `monitoring_groups(org_id, parent_id, name, selector jsonb, membership_mode
within_parent|independent, priority int default 100, status_page_display)`,
`monitoring_group_members(group_id, host_id, source selector|manual|excluded)`,
`check_definitions(org_id, plugin, params jsonb, interval, binding {org|group|host},
probes jsonb, enabled, origin seed|user)`, `check_probe_results(item_id, probe_id,
state, last_value, at)`, `check_items(host_id, definition_id, item_key, discovered_at, ignored)`,
`rules(org_id, scope {org|group|host|item}, conditions jsonb, expression, severity,
for_duration)`, `check_states(item_id, state, since, last_value, flapping)`, `metrics
(time, item_id, name, value)` (hypertable, continuous aggregates 1 m / 5 m / 1 h).

Alerting: `alerts(org_id, item_id, severity, opened, acked_by, resolved)`, `channels
(org_id, kind, config jsonb encrypted)`, `schedules(org_id, name, windows jsonb)`,
`routes(group_id|org, schedule_id, min_severity, channel_ids)`, `escalations(route_id,
after, channel_ids, repeat)`, `maintenance_windows(org_id, target host|group|org, start, end, recurrence, note,
visibility internal|public, public_title, public_text)`, `maintenance_publications
(window_id, page_id)`, `notifications(alert_id, channel_id, sent_at, result)`.

Status pages: `status_pages(org_id, slug, custom_domain, theme jsonb)`, `status_components
(page_id, target host|group|item, display_name, order)`, `incidents(org_id, title,
state, origin_alert_id, updates jsonb)`, `incident_publications(incident_id, page_id,
component_ids)`, `subscribers(page_id, email, confirmed)`.

Secrets at rest (SSH keys, channel tokens, Git credentials, schema fields of type
`secret`) are encrypted with an instance master key (`PULSIX_MASTER_KEY`, 32 bytes) using
AES-256-GCM with per-row nonces; the key never enters the database.

## 5. Host capabilities

A host has two independent boolean capabilities:

- `managed`: has SSH connection data and may be a member of inventory groups; Ansible
  jobs can target it. No agent required. SSH data = user, port, credential and an
  optional **jump host** (bastion). Bastions are org-level objects (address, port, user,
  credential, optionally reached through another bastion) and resolve by inheritance:
  host override → inventory group (walking up the tree) → zone default → none. The
  inventory snapshot renders this as `ansible_ssh_common_args` with `ProxyJump`
  (chained bastions become `-J a,b`). Worker remote checks do not use SSH and are not
  affected.
- SSH credentials of kind `key` are generated by Pulsix (ed25519; private key sealed,
  never displayed) or imported; the **public key** is always visible with a comment
  `pulsix:<org>:<credential>` and ready-made snippets (`authorized_keys` one-liner,
  cloud-init, Terraform). Kind `password` covers hosts without key login (Windows
  via OpenSSH or WinRM, appliances). Optional one-time **bootstrap**: with an existing
  login (e.g. provider root password, used once and not stored) the worker runs the
  built-in `pulsix.bootstrap` playbook that creates the `ansible` user, installs the
  public key and sudo rule.
- **Connection test** from the zone worker (also re-run daily and after rotation):
  reachability through the jump host, host key trust-on-first-use with the
  fingerprint shown for manual comparison, then pinned per host (`hosts.ssh_host_key`);
  a changed host key raises a critical alert and blocks jobs until re-trusted; login,
  passwordless sudo (or become password), Python presence, fact gathering. Result is
  shown as `SSH ok` / error in the host list.
- `monitored`: has an enrolled agent and/or remote check definitions bound to it. No SSH
  data required.

The host list filters and shows both; the host detail shows tabs only for active
capabilities. Enabling `monitored` without an agent gives remote checks only (ICMP, TCP,
HTTP, TLS, DNS, SNMP from the zone worker).

## 6. Inventory groups and the variable schema builder

Inventory groups form a tree per org. Each group owns a **schema**: an ordered list of
fields. Field definition:

```json
{
  "name": "vhosts", "label": "Virtual hosts", "type": "repeater",
  "scope": "host", "required": true, "min_items": 1,
  "description": "Virtual hosts served by this host",
  "fields": [
    {"name": "domain", "type": "string", "required": true, "pattern": "^[a-z0-9.-]+$"},
    {"name": "php_version", "type": "enum", "options": ["8.2", "8.3", "8.4"]},
    {"name": "ssl", "type": "bool", "default": true},
    {"name": "aliases", "type": "list", "item": {"type": "string"}},
    {"name": "basic_auth", "type": "object", "fields": [
      {"name": "user", "type": "string"}, {"name": "password", "type": "secret"}]}
  ]
}
```

Types: `string`, `int`, `float`, `bool`, `enum`, `secret`, `list<T>`, `repeater`
(list of objects with a sub-schema), `object` (single sub-schema). Common attributes:
`label`, `description`, `required`, `default`, `scope` (`group` | `host`), `overridable`
(for group-scoped fields: a host may override), validation (`min`, `max`: a number range
for `int`/`float`, a length in characters for `string`; `pattern`; `min_items`,
`max_items`: item count for `list`/`repeater`).

Naming: field names match `^[a-z_][a-z0-9_]{0,63}$` and a top-level field name must not
start with `ansible_` or `pulsix_`, so a schema can never shadow a connection variable or
the facts. Group names match `^[a-z_][a-z0-9_]{0,62}$` (a valid Ansible group name), must
not be `all` or `ungrouped` (Ansible's implicit groups) or `_meta` (the dynamic inventory's
`_meta.hostvars` block, which a group of that name would overwrite), and are unique per
organization because Ansible's group namespace is flat.

Values

- Group-scoped values live on the group (`inventory_groups.vars`).
- Host-scoped fields are asked only for hosts assigned **directly** to that group
  (`inventory_memberships.vars`); a host inherits its ancestors' group-scoped values
  through Ansible's own group inheritance, but does not answer an ancestor's host-scoped
  fields unless it is a direct member of that ancestor too.
- Validation runs server-side against the schema on every write; the SPA renders the form
  from the same schema JSON as a controlled component (§15), not a static form.
- Schema changes: adding an optional field is free; adding a required field marks
  affected memberships as `incomplete` and the GUI lists them; removing a field keeps the
  stored value until the next save (never destroys data silently); the next save sends
  only the fields the schema still declares, which drops any orphaned ones.
- **Secret wire format.** Stored inside a vars document as `{"$sealed": "<ciphertext>"}`.
  Reads return `{"$secret": "set"}`, never the plaintext. A write sends a plaintext string
  for a new value, or `{"$secret": "keep", "from": "<path>"}` to keep the value already
  stored at `<path>`, so a repeater entry that moves keeps its secret. A client can never
  write `$sealed` directly.
- **Hosts in several groups.** Membership values merge in order of group depth (shallow
  first), then group name; the later group wins, and a key set to different values by two
  groups produces a warning in the inventory preview.

Export to Ansible

The worker requests an inventory snapshot for a job (`GET /internal/inventory/{job_id}`).
The manager renders a dynamic-inventory JSON: groups with `children`, `hosts`, and
`vars`; host vars merge group-scoped values (walking the tree root → leaf), then host
overrides, then membership values, then host facts under `pulsix_facts`. Secrets are
decrypted only inside that snapshot, delivered over the mTLS channel, and never written to
the worker's disk (ansible-runner reads them from memory via an inventory script).

## 7. Ansible execution

- Content sources: Git repos per org (SSH key or token credential, branch, optional
  subpath). Workers clone into a cache, `git fetch` before each run, install
  `requirements.yml` (collections and roles) into a per-repo path. Ad-hoc playbooks from
  the GUI editor are stored in the database and materialized into a temp dir per run.
- Job template = playbook + inventory scope (whole org, group, host list) + `limit` +
  extra vars (typed by an optional schema like groups) + SSH credential + zone selection
  (auto: the zone of the targeted hosts; a job spanning zones is split into one job per
  zone) + triggers.
- Triggers: **manual** (button, API), **schedule** and **incoming webhook**. Every run
  records `jobs.trigger` (`manual`, `schedule`, `webhook`, `system`) and `trigger_ref`
  (the schedule or webhook trigger), and the audit log names its actor: a user, a
  token, `schedule:<id>`, or `webhook:<id>` with the source address.
- Schedules (v0.2c): cron (5 fields and `@hourly`, `@daily`, `@weekly`, `@monthly`;
  day-of-month and day-of-week combine with OR) or a simple mode (every N minutes or
  hours, daily, weekly, monthly) compiled to cron; an IANA time zone with defined DST
  behaviour (a time in the spring-forward gap runs at the first valid minute after it,
  a repeated fall-back time runs once); guards `overlap` (skip or queue) and
  `catch_up` (enabled, window 1 min–24 h); per-schedule `limit` and extra-var
  overrides validated against the template schema, secret values sealed; pause,
  resume, run now, pause all. A ticker (`PULSIX_SCHEDULE_TICK`, default 15 s) takes
  due schedules with `FOR UPDATE SKIP LOCKED`, so with several managers each
  occurrence is decided once; enqueueing is at-least-once. Every decision is kept in
  `schedule_runs` (`triggered`, `skipped_overlap`, `missed`, `failed`). Scheduled runs
  pass the same enqueue validation as manual runs. The maintenance-window guard
  arrives with maintenance windows (v0.2d).
- Incoming webhooks (v0.2c): `POST /api/v1/hooks/jobs/{id}` runs a template and
  `POST /api/v1/hooks/repos/{id}` syncs a repository (the push webhook of 2.8).
  Presets: Pulsix, GitHub and Gitea (HMAC-SHA256 of the raw body), GitLab and bearer
  (token). An optional timestamp header limits replays to ±5 minutes, and the HMAC
  then covers `<timestamp>.<body>`. An IP allowlist applies to the client address that
  `PULSIX_TRUSTED_PROXIES` resolves. The mapping takes an RFC 9535 JSONPath subset (`$`,
  names, indexes, `[*]`) from the JSON payload to extra vars and `limit`, validated
  like a manual run; secret-typed fields and the connection-security variables cannot
  be mapped. Mapped strings reach Ansible marked unsafe (never templated) and a mapped
  `limit` must be a plain host pattern (§14). A per-trigger token bucket is shared by
  all managers; a per-address limit counts only calls that fail with the uniform 404.
  `on_active` either queues or skips. A trigger keeps its last
  500 deliveries (`trigger_deliveries`) without the body.
- Schedule notifications (v0.2d): rules per schedule (`on failure`, `on change`, `on
  success`, `skipped/missed` → channels from §11), an optional weekly digest, and a
  calendar of scheduled runs together with maintenance windows.
- Queue: `jobs` rows in `queued` state. Workers of the matching zone poll with
  `SELECT … FOR UPDATE SKIP LOCKED` through the manager's gRPC `ClaimJob`; the manager
  owns the SQL, workers never talk to Postgres.
- Execution: worker runs `ansible-runner` as a subprocess with a private env, streams
  events (`runner_on_ok`, `runner_on_failed`, task starts, stdout lines) over the gRPC
  stream; the manager stores `job_events` and pushes them to the SPA over Server-Sent
  Events. Per-host results, retry-failed-hosts, cancel.
- Worker image pins Ansible core and ansible-runner; per-org Python requirements are not
  supported in v1 (collections yes, pip packages no).

## 8. Agent

- Single static Go binary; Linux amd64/arm64 (systemd unit), Windows (service), macOS
  (launchd daemon). Same code, OS-specific collectors behind build tags.
- Enrollment: operator creates an enrollment token in the GUI (bound to org, zone,
  optional tags, single-use or limited count, expiry). Install:
  `curl -fsSL https://<manager>/install.sh | sh -s -- --token <t>` (PowerShell equivalent
  on Windows). The agent generates a key pair, sends a CSR with the token to the worker,
  which forwards it to the manager CA; the agent receives a client certificate (90 d,
  auto-renewed at 2/3 lifetime) and the CA bundle. From then on: mTLS only.
- Connection: one persistent gRPC stream to the zone worker (address list in the
  enrollment response; failover to the next worker in the zone; exponential backoff).
  The agent pushes raw metric samples and, for the `script` plugin, the state the script
  reported; the worker pushes the resolved check definitions for that host. Rules are not
  sent to agents: threshold evaluation happens in the manager (section 9).
- Local config `/etc/pulsix/agent.yaml` (or platform equivalent):

  ```yaml
  server: fra-1.pulsix.acme.io:8443
  secrets:
    pg_main: { dsn: "postgres://monitor:***@localhost/postgres" }
    mysql_shop: { user: monitor, password: "***" }
  checks:                      # optional static local checks
    - plugin: script
      command: /usr/local/lib/pulsix/check_backup.sh
      interval: 1h
  ```

  Secrets never leave the host. The agent reports secret **names** in its status so the
  GUI can flag a check that references a missing secret.
- Configuration reaches the agent two ways: the worker pushes changes over the open
  stream, and the agent re-fetches its complete configuration every 60 s (configurable
  30-600 s) and applies the diff, so a missed push heals itself. Each configuration
  carries a version; the GUI shows how many agents are on the current one.
- Every agent binary contains the **probe** for synthetic checks (HTTP, TCP, TLS, DNS,
  ICMP), so any monitored host can serve as a vantage point with no extra install.
- Facts: OS, kernel, CPU, memory, disks, interfaces, IPs, virtualization, agent version.
  Reported at start and every 6 h; selected facts become auto tags (`os=…`, `arch=…`,
  `virt=…`).
- Self-update (later milestone): worker offers a signed binary; the agent verifies the
  signature with a key baked into the binary.

## 9. Check engine

Layered like Checkmk rulesets and Zabbix templates.

1. **Plugins (code).** Implemented in the agent (local) or worker (remote). Each plugin
   declares: `name`, `location` (agent | worker), a **parameter schema** (same field
   format as the variable builder), the **metrics** it emits (name, unit, type), whether
   it **discovers items**, and default rules. v1 plugins: `cpu`, `memory`, `disk`,
   `inodes`, `network`, `process`, `service` (systemd/Windows/launchd), `heartbeat`,
   `smart`, `docker`, `postgres`, `mysql`, `redis`, `nginx`, `apache`, `phpfpm`, `script`
   (agent); `icmp`, `tcp`, `http` (with JSONPath extraction), `tls`, `dns`, `snmp` (worker).
2. **Check definitions (data).** `plugin + params + interval + binding`. Binding targets
   the org (all monitored hosts), a monitoring group, or a host. Pulsix ships a seed set
   (`Linux base`, `Windows base`, `macOS base`, `PostgreSQL`, `MySQL`, `Web server`,
   `Docker host`, `Endpoint`) as data with `origin = seed`; orgs can disable, copy or
   edit them. Agents receive the resolved definitions for their host.
3. **Probes (where a check runs).** Host-local plugins always run on the host's own
   agent. Synthetic plugins (`http`, `tcp`, `tls`, `dns`, `icmp`, `snmp`) carry a list
   of **probes** instead: the **manager** (built-in), one or more **zone workers**,
   and/or an **agent group** (a monitoring group whose agents act as vantage points).
   Every probe reports its own result and metrics, so latency and availability are
   visible per location. A **fail policy** per check decides when the check as a whole
   is bad: `any probe`, `majority`, or `all probes`, with an optional confirmation step
   that requires a second probe to agree before an alert opens.
4. **Web and port assertions.** An HTTP check defines a request (method, URL, headers
   with `{{ secret.<name> }}` references, body, redirect handling, timeout, TLS
   verification, HTTP version, maximum body read) and a list of **assertions**, each
   with its own severity: status code, response time (total, with a DNS / TCP / TLS /
   TTFB / transfer breakdown stored as metrics), body text contains or not contains,
   regex, HTML selector exists or matches (HTML5 parser), JSON path compare, response
   header compare, redirect count, content size, TLS days until expiry, TLS issuer.
   TLS validation covers chain, hostname and expiry. A `tcp` check verifies the
   connection and optionally matches a banner. Failed assertions appear on the alert
   with the value that was actually seen.
5. **Discovery.** Plugins that enumerate items (mounts, units, interfaces, containers,
   databases) report them; each becomes a `check_item` under the definition. New items
   inherit rules, can be ignored, and can carry item-level overrides.
6. **Rules.** A rule maps metrics to a state:

   ```
   scope: org | group(selector) | host | item(key)
   conditions: tags / item key filters
   form:       metric, operator, value, for_duration, severity
   expression: optional, replaces form, e.g.
               avg(disk.used_pct, 5m) >= 90 or disk.days_until_full < 7
   ```

   Both modes compile into the same expression AST. Evaluation happens in the manager
   (workers and agents ship raw values), on every incoming sample plus a periodic sweep
   for `for_duration` and missing data. Precedence: item > host > group > org > plugin
   default; among groups the deepest one wins, a tie between groups at the same depth
   is broken by the group's `priority` (higher wins), and an unresolvable tie is
   reported in the effective-rules view instead of being decided silently; the first
   matching rule per severity wins; the GUI shows the **effective
   rules** per item with the source of each. Hysteresis: a rule can define a clear value
   (`>= 90` triggers, `< 85` clears); flapping is detected from state changes per 30 min
   and suppresses notifications while showing the flapping flag.
7. **States.** `ok`, `warn`, `crit`, `unknown` (no data within 3 × interval or plugin
   error). Host state is the maximum of its item states; group state likewise.
8. **Custom checks.** The `script` plugin runs a local command and parses either the
   Nagios plugin protocol (exit code 0/1/2/3 → ok/warn/crit/unknown, optional perfdata)
   or a JSON document (`{"items": [{"key": …, "state": …, "metrics": {…}}]}`). Scripts are
   configured in the GUI (path, args, timeout, secret references) or in the local
   `agent.yaml`. The agent refuses scripts outside an allow-listed directory unless the
   local config opts in.

## 10. Time series and retention

- Metrics go into a TimescaleDB hypertable partitioned by time (1 d chunks), compressed
  after 3 d; continuous aggregates at 1 m, 5 m and 1 h drive graphs beyond 24 h.
- Retention per org: raw 30 d (configurable 7–90), 5 m aggregates 90 d, 1 h aggregates
  365 d (configurable up to 3 y). Implemented with per-org retention jobs.
- Ingest: workers batch samples (up to 500 or 2 s) into one gRPC message; the manager
  writes with `COPY`. Target: 5 000 agents at 30 s intervals with ~40 metrics each
  (~7 000 samples/s) on one Postgres node.
- Graphs: the API serves downsampled series (`/metrics/query` with step and range); the
  SPA renders with uPlot.

## 11. Alerting

- **Monitoring groups**: membership = tag selector (`key = value`, `key in (…)`,
  `key exists`, combined with AND; OR by creating another group) plus manual members
  and explicit exclusions. Re-evaluated when tags change. A group carries check
  definitions, rules, routes, maintenance windows and the status page mapping.
- Groups are **many-to-many**: a host belongs to any number of them. They may also be
  **nested** through an optional parent, which serves inheritance rather than
  exclusivity. A child's `membership_mode` is `within_parent` (the selector is applied
  to the parent's members, the usual case) or `independent` (applied to every host of
  the organization). Inheritance differs by kind on purpose: **alert rules override**
  (deepest group wins, `priority` breaks ties), while **check definitions, notification
  routes and maintenance windows add up** across every group a host belongs to, with
  notifications deduplicated per channel and alert. A host therefore never loses a
  notification by joining another group.
- **Alerts** open when an item enters `warn` or `crit` after `for_duration`, close on
  `ok`, and record every state change. Acknowledge (with note), snooze (until time or
  until state changes), silence (host, group, or item, with duration).
- **Routes**: per group (or org default): schedule (time windows by weekday and time,
  timezone) + minimum severity + channel list. Several routes per group. Digest option for
  a route (collect warnings and send at a time).
- **Escalation**: unacknowledged critical after N minutes → further channels, repeat every
  M minutes, stop on ack or resolve.
- **Channels**: email (SMTP), generic webhook (HTTP method, headers, body template with
  alert fields), Telegram bot, Slack, Mattermost, ntfy, Pushover, Twilio SMS, PagerDuty
  Events v2. Each channel has a test button. Delivery is a manager-side job queue with
  retries and a per-alert notification log.
- **Maintenance windows**: created where they act — on a host, a monitoring group or the
  org — or from the central Monitoring → Maintenance list with a target picker. Start/end
  or recurring; alerts on the target are silenced and marked "in maintenance". Each window
  has a visibility: `internal` (silence only) or `public` with a public title and text
  and a list of status pages it is announced on. Ends with a summary of states.

## 12. Status pages

- Per org, many pages. Served by the manager under `https://<slug>.<status-domain>` and
  optional custom domains (ACME via the operator's reverse proxy; the manager only maps the
  Host header).
- Components: hosts, monitoring groups or single items with a display name and order.
  A component's state is the aggregated state of its target; `warn` shows as "degraded
  performance", `crit` as "outage" (labels editable per page).
- 90-day history bars from daily state summaries, uptime percentage per component.
- Incidents belong to the org and are published to one or more pages, each with its own
  affected components. Fields: title, status (investigating, identified, monitoring,
  resolved), timestamped updates in Markdown, optional link to the originating alert.
  Three entry points create the same object: the status page admin, "Create incident"
  on an alert (components pre-filled from the alert's host or group), and the host or
  monitoring group page. Scheduled maintenance is shown from maintenance windows with
  `public` visibility on that page.
- Subscriptions: email with double opt-in, notified on incident updates and maintenance.
- Never exposes hostnames, IPs, metric values or check names. Public pages are rendered
  server-side (no SPA), cacheable for 30 s, with a small theme (logo, colours) per page.

## 13. Scaling and high availability

- Manager instances are stateless: sessions in signed cookies + a `sessions` table for
  revocation, SSE fan-out via Postgres `LISTEN/NOTIFY`. Run N instances behind a load
  balancer; the scheduler and alert sweeper use a Postgres advisory lock so exactly one
  instance runs them at a time (automatic takeover).
- Workers register per zone with a name and capabilities (`ansible`, `checks`,
  `gateway`). Any number per zone. Jobs and remote checks are claimed with `SKIP LOCKED`;
  agent connections are spread by the enrollment address list. When a worker disappears
  (missed heartbeats for 60 s), its remote checks are re-claimed by another worker of the
  zone and its agents reconnect on their own.
- Postgres is the single stateful service. HA is the operator's concern (managed
  Postgres, Patroni); the manager only needs a connection string and handles failover
  reconnects.
- Sizing for v1: 10 000 hosts per installation, 500 hosts per worker, 100 orgs.

## 14. Security

- All transport TLS 1.3. Agents and workers authenticate with client certificates from
  the manager's internal CA; the manager's serving certificate comes from the operator's
  reverse proxy or ACME.
- Enrollment tokens are single-purpose, expiring, and bound to org and zone; the CSR
  carries the host name, the certificate carries `org_id` and `zone_id` in a SAN URI.
- Secrets at rest encrypted with the master key; secrets in transit only inside inventory
  snapshots to workers; agent secrets never leave the host.
- Job execution isolation: ansible-runner runs as an unprivileged user in the worker
  container; per-job temp dirs are wiped after the run; `ANSIBLE_*` env is fixed.
- **Worker trust model.** Anyone who may run jobs in an organization (operators and
  above) can run arbitrary code on that organization's workers by design: a playbook
  task can target `localhost`. Such code can reach everything the worker process
  reaches: the files of the running job (inventory with revealed secrets, key files,
  extra vars — all 0600 in the job's work dir, wiped afterwards), the repository cache
  of its organization, the worker's own client certificate and key (so it can act as
  that worker: claim further jobs of the same organization and zone, including their
  revealed credentials), and the network the worker sits in. It cannot reach the
  manager's database, the master key or another organization: a worker is enrolled for
  one zone of one organization, the manager only hands it that zone's jobs and accepts
  results only for jobs it claimed, and the default deployment runs workers in their
  own container without database access or master key. Consequences: grant `jobs.run`
  only to people you would trust with every credential the organization's hosts use;
  give each trust domain its own organization (or at least its own zone and worker).
  Running playbooks under a separate uid so a task cannot read the worker's key is a
  hardening item (backlog).
- **Worker enrollment (v0.2c).** An owner or admin creates a token
  `pxw1.<id>.<secret>.<ca-sha256>`: single use, valid for one hour, bound to
  organization, zone and worker name; only the SHA-256 of the secret is stored, and
  it is compared in constant time. The worker creates its key locally (it never
  leaves the host), sends a CSR over a connection pinned to the CA fingerprint in the
  token, and receives a 90-day certificate whose CN and organization/zone URI SANs
  the CA sets itself (CSR subject and extensions are ignored). It renews over its
  authenticated connection once a third of the validity is left, keeping its key.
  `Enroll` is the only RPC callable without a client certificate and is limited to
  10 calls per minute and address. Revocation marks the worker and every serial it
  ever held as revoked, ends its open event streams and requeues its jobs; unknown
  and revoked serials are `Unauthenticated`, and a certificate whose zone differs from
  the registry row is refused. A revoked worker cannot come back under a recreated
  row, because its serials stay revoked.
- **Incoming webhooks (v0.2c).** The hook endpoints are the only unauthenticated
  write paths besides login, setup and enrollment: body ≤ 1 MiB, constant-time
  signature and token compares, one identical empty 404 for unknown, disabled,
  disallowed-address and bad-signature calls, per-trigger and per-address rate
  limits, secrets sealed with AAD `trigger:<org>:<id>` and shown only at creation and
  rotation, and no secrets or bodies in URLs, logs, audit entries or delivery rows.
  Webhook and scheduled runs pass the same validation as manual runs (schema, the
  connection-security denylist, host-key pins). Outbound webhooks (notification
  channels, with SSRF rules) are v0.2d.
- **Webhook payloads are untrusted input (v0.2c final review).** A valid signature
  proves who delivered a payload, not who wrote its values: a GitHub push is signed
  whatever branch name the pusher chose, and a pull request carries a fork author's
  title. So every string a mapping takes from a payload reaches Ansible marked unsafe,
  at any depth of lists and maps (`{"__ansible_unsafe": "<s>"}` in the extra-vars
  document, the JSON form of `!unsafe`): Jinja expressions and lookups in it stay
  literal text instead of running on the worker. The marker travels sealed with the
  run's overrides (`RunOverrides.Untrusted`) and is applied at claim, so a worker
  never has to know where a value came from; redaction still reads the plain values.
  Dictionary keys are never templated by Ansible and need no marker, **but the marker
  only holds while Ansible loads the extra-vars document as JSON**: an object key it
  reserves (`__ansible_unsafe`, `__ansible_vault`, `__ansible_type`) that does not
  decode makes the JSON load fail, ansible-core silently re-reads the file as YAML,
  and every marker in it becomes a plain dict whose strings are templated again
  (v0.2c final re-review). So a mapped value holding an object key that starts with
  `__ansible`, at any depth, answers 422 naming its mapping target field at receive
  time and fails the claim as a second line; and an untrusted run also fails its
  claim when any object in the merged document (the template's own values included)
  has an `__ansible` key without being a well-formed `{"__ansible_unsafe": "<s>"}` or
  `{"__ansible_vault": "<s>"}` marker. The "any depth" guarantee holds only with
  these refusals in place. A mapped `limit`
  must be a plain host pattern — host and group names (letters, digits, `.`, `_`,
  `-`) with the operators `,` `:` `!` `&` and the wildcard `*`; anything else, above
  all `@file` (read a pattern list from a file on the worker), `~regex` and
  `[subscripts]`, answers 422 naming `limit` at receive time and fails the claim as a
  second line. **Schedule overrides, template values and manual runs are not marked
  and keep the full limit syntax:** they come from authenticated operators
  (`templates.write` / `jobs.run`), who can already run arbitrary code on the worker
  through a playbook (worker trust model above), so marking them would protect nothing
  and break legitimate templating.
- **Hook rate limits and proxies.** The per-address limit on the hook endpoints counts
  only calls that end in the uniform 404 (unknown, malformed or wrong-endpoint id,
  disabled trigger, disallowed address, bad signature); once an address has spent its
  budget, all of them answer the same 429. A delivery that proves the secret is never
  charged or refused by it — the per-trigger token bucket limits those — so an
  anonymous flood cannot starve genuine senders that share its address. Behind a
  reverse proxy the manager sees the proxy as every caller unless
  `PULSIX_TRUSTED_PROXIES` names it: allowlists then never match and audit entries
  record the proxy. The compose stack gives its bundled Traefik a fixed address outside
  the network's dynamic range and trusts exactly that address by default.
- **Run output redaction.** Run logs are readable with `jobs.read`. The worker removes
  every secret value the job revealed — credential secrets (keys, passwords, tokens,
  become passwords), secret-typed extra vars, secret inventory variables, one-time
  bootstrap logins — from every event before it leaves the worker, as literal strings,
  their JSON-escaped form and, for keys, line by line; redaction runs before any size
  truncation. Values shorter than 4 characters are not redacted, and a secret a task
  transforms (base64, hashes, substrings) cannot be recognized — `no_log: true` remains
  the playbook author's tool for those. Each ansible event is stored once, reduced to
  host, task, module, result and display text.
- **Host keys.** A host's key is pinned from a connection test (fingerprint and full
  key). Every job that connects to a pinned host — runs, ad-hoc commands, bootstrap,
  key rotation and revocation — checks the key strictly on the very connection the
  playbook uses, through its jump hosts; a changed key fails the connection, and once
  a connection test reports the change such jobs are refused until a test confirms a
  pinned key again. Unpinned hosts and jump hosts are trusted on first use for the
  duration of one job only (jump hosts carry no pin yet; a pinned target is still
  verified end to end through them). The connection-security variables (host key
  checking, the SSH argument lists, the SSH executable, the connection plugin, the
  paramiko host-key options, the `pulsix_` namespace) are refused in template extra
  vars (422) and at claim time, inventory schemas reserve the `ansible_`/`pulsix_`
  prefixes, and the worker forces them as extra vars — Ansible's highest precedence,
  above inventory, play and task vars — with each host's pinned options read from
  `pulsix_ssh_common_args`. A playbook that redefines `pulsix_ssh_common_args` in its
  own vars can still weaken its own run: playbook authors run code on the worker by
  design (above).
- Rate limits on auth endpoints, WebAuthn user verification required, TOTP with
  replay protection, API tokens hashed (argon2id) at rest.
- OWASP audit before v1.0 (`owasp-audit` skill).

## 15. API and frontend

- REST, JSON, versioned under `/api/v1`. OpenAPI 3.1 generated from Go handler
  annotations (`huma` or `ogen`; decision in the plan), served at `/api/openapi.json`;
  explorer at `/api/explorer` (Scalar). Cursor pagination, `If-Match` for optimistic
  concurrency on config objects, consistent problem+json errors.
- Real-time: SSE endpoints for job logs, alert feed and host state changes.
- SPA: React 19, TypeScript, Vite, TanStack Router and Query, shadcn/ui on Tailwind,
  uPlot for graphs. Forms rendered from a schema (group assignment, check parameters,
  job extra vars) are controlled components over a plain value tree, not
  react-hook-form: the schema is only known at runtime and nests to arbitrary depth, and
  one recursive renderer over a value tree is simpler to test than nested field-array
  hooks. react-hook-form stays for static forms (auth, invites, settings). Dark-first
  with the brand tokens; light theme later. ⌘K command palette for global search.
- The SPA is built into the manager binary (`embed`), so one image serves everything.

## 16. Deployment

- Images: `noixdev/pulsix-manager`, `noixdev/pulsix-worker`, plus agent binaries
  attached to GitHub releases. Multi-arch (amd64, arm64). Also pushed to
  `harbor.cloud.noidee.dev/noixdev/…` for internal deploys via Portainer.
- `docker-compose.yml` for single node: `db` (timescale/timescaledb-ha), `manager`,
  `worker-enroll` (one-shot enrollment into a bundle volume) and `worker` (the default
  zone's worker, no database or master-key access); further workers per zone are
  enrolled with `pulsix-manager worker add`. The manager's embedded worker is opt-in
  (`PULSIX_EMBEDDED_WORKER`, trusted single-tenant setups only). Configuration by
  environment variables (`PULSIX_DATABASE_URL`, `PULSIX_MASTER_KEY`, `PULSIX_PUBLIC_URL`,
  `PULSIX_STATUS_DOMAIN`, SMTP settings).
- Migrations run on manager start (goose), guarded by an advisory lock; additive only.
- Backups: `pg_dump` guidance and a `pulsix-manager backup` command that also exports the
  CA and master-key-encrypted secrets bundle.

## 17. Repository layout

One repository `NoiXdev/pulsix` (private until v1.0):

```
cmd/manager, cmd/worker, cmd/agent          entry points
internal/{auth,api,inventory,ansible,checks,metrics,alerting,statuspage,ca,grpc,db}
plugins/                                     check plugins (agent + worker)
proto/                                       gRPC definitions (buf)
web/                                         React SPA
brand/                                       logo, tokens
deploy/compose, deploy/systemd, deploy/install.sh
docs/                                        BACKLOG.md, specs, ADRs, user docs
```

Dependency policy: every dependency runs on its newest stable release, or the active
LTS line where the project publishes one; deprecated APIs are never introduced and are
replaced when a library deprecates them. Versions named in this document are the state
at the time of writing, not a pin.

Versioning: **one version for everything.** Manager, worker and agent share the
module's version; a single tag `vX.Y.Z` on `main` produces both images and every agent
binary in one release. Compatibility: the manager accepts workers and agents of the
same minor version and the previous one (`v0.4.x` talks to `v0.3.x`), so upgrades roll
manager → workers → agents (agent self-update from v0.3 later item); older components
are rejected and shown as "update required" in the GUI. No per-component tags, no
separate agent repository.

Conventions: Go 1.27, `golangci-lint`, `sqlc` for queries, `goose` migrations,
Conventional Commits with a CI gate, Pint-equivalent formatting via `gofmt`/`prettier`,
tests with Go's testing package (integration tests against a Postgres container) and
Vitest/Playwright for the SPA.

## 18. Testing strategy

Every feature ships with tests; a pull request without tests for new behaviour does not
merge. Coverage is enforced in CI (Go packages ≥ 80 % lines, critical packages listed
below ≥ 90 %, SPA components ≥ 70 %). Tests run on every PR; the end-to-end suite runs
on every merge to `main` and nightly against the multi-arch images.

Layers

1. **Unit** (Go `testing`, Vitest). Mandatory table-driven tests for the critical
   packages: schema validator and form renderer, rule expression compiler and evaluator
   (including hysteresis and `for_duration`), tag selector matcher, inventory renderer
   (group tree merge, bastion resolution, secret injection), alert routing and escalation
   timing, status aggregation, check plugin parsers (Nagios protocol, JSON, SMART, DB
   outputs), TOTP/WebAuthn flows with fake authenticators.
2. **Integration** (testcontainers-go with Postgres + TimescaleDB). Migrations up and
   down, RLS isolation between two orgs, metric ingest with `COPY`, continuous
   aggregates and retention jobs, job claiming with `SKIP LOCKED` under concurrency,
   advisory-lock takeover for the scheduler, notification delivery against mock SMTP,
   webhook and chat endpoints, CA issuance and certificate renewal.
3. **Protocol** (gRPC). Agent ↔ worker and worker ↔ manager streams tested with an
   in-process fake on each side: enrollment handshake, reconnect with backoff, config
   push, batching, back-pressure, certificate rotation without dropping samples.
4. **Agent per OS** (CI matrix: Linux amd64/arm64, Windows, macOS runners). Every
   collector and service plugin is tested against real OS APIs on the runner; fixtures
   for hardware outputs (SMART, RAID) where CI runners cannot provide the device.
5. **End-to-end** (Compose stack in CI, Playwright for the SPA, Go harness for the
   backend). Scenarios that must pass before a milestone closes:
   - Enroll a containerized agent with a one-time token; assert certificate issued,
     facts received, default checks applied, first metrics within 60 s.
   - Monitoring pipeline: fill a disk in the agent container past a rule threshold;
     assert item state, alert opened after `for_duration`, notification sent to a mock
     channel, alert resolved and closed after cleanup; verify hysteresis and flapping
     suppression.
   - Remote checks: worker checks an HTTP endpoint and a TLS certificate near expiry;
     assert states and alert.
   - Ansible: run a playbook from a Git fixture repo against an SSH container through a
     bastion container; assert per-host results, live log streaming and a failed-host
     retry.
   - Schema builder: create a group schema with repeater and sub-schema, assign a host,
     validate errors, render the inventory snapshot and compare with a golden file.
   - Multi-tenancy: two orgs, assert no data leaks through API, SSE or status pages.
   - Status page: component aggregation, maintenance visibility, incident publish and
     subscriber e-mail, public page never exposes hostnames.
   - HA: two manager instances behind a proxy; kill the scheduler holder and assert
     takeover; kill a worker and assert agent reconnect and remote-check re-claim.
   - Auth: passkey registration and login with a virtual authenticator, TOTP, recovery
     code, API token scopes.
6. **Load** (nightly, not per PR). 1 000 simulated agents at 30 s intervals against one
   manager and one Postgres; assert ingest latency and no sample loss.

Tooling: `go test -race`, `golangci-lint`, `sqlc` generated code checked in and diffed,
`buf lint`/`buf breaking` for protos, Vitest + Testing Library, Playwright with trace
on failure, OpenAPI contract tests (Schemathesis-style fuzzing against the running
manager). Test data through factories, never shared fixtures that couple tests.

## 19. Milestone mapping

See `docs/BACKLOG.md`. v0.1 Core → v0.2 Inventory & Ansible → v0.3 Agent & monitoring
(check engine included) → v0.4 Alerting → v0.5 Status pages → v0.6 Scale & HA → v1.0.

## 20. Open questions

- OpenAPI toolchain: `huma` (schema-first handlers) vs. `ogen` (spec-first codegen).
  To be decided in the implementation plan after a spike.
- Whether the manager should offer an optional embedded ACME client for status page
  custom domains, or leave TLS to the reverse proxy entirely (v1: reverse proxy).
- Agent self-update signing key management (v0.3 later item).
