# Probe vantage points (backlog 3.13a) — design

Status: the owner accepted the proposal on 2026-11-16 ("passt"): synthetic
checks gain vantage points — the manager (as today) and the agents of
monitoring groups — with per-vantage results and a fail policy; the
existing metrics of a check carry the combined verdict, so rules, alerts,
seed rules and the fleet view stay unchanged; per-vantage results get their
own current values and history, shown on the host page, with no rules on
them in this cut; agents get probe assignments as a message of their own,
at most `agents_per_group` per group (default 3), never the target host
itself; checks with a `secret` may not use agent vantages; a monitoring
group used as a vantage cannot be deleted. Split out as new backlog rows:
zone workers as vantage points, confirmation by an immediate re-probe from
another vantage, and rules on single vantages. Every decision below not
listed here is **(Claude's call)**.

## Goals

- A synthetic check (`icmp`, `tcp`, `http`, `tls`, `dns`, `snmp`) can be
  probed from several places at once: the manager and agents chosen from
  monitoring groups.
- Every vantage's result is visible on its own (status, response time,
  history), so latency and reachability show per location.
- A fail policy per check (`any`, `majority`, `all`) decides when the check
  as a whole is down. The check's existing metrics carry that verdict.
- A check that names no vantage group behaves exactly as today, byte for
  byte: no new rows, no new writes.

## Non-goals

Zone workers as vantages; confirmation by an immediate re-probe; rules,
alerts or states per vantage; probing from agents with credentials
(secrets stay on the manager); vantages for agent-run plugins; an agent
choosing its own targets.

## Parameters (`internal/checkplugins`)

Every synthetic descriptor (the ones with `Runs: Server`) gains four
parameters through one helper (`vantageParams()` in a new
`vantage.go`), appended after the plugin's own:

| Param | Type | Default | Meaning |
|---|---|---|---|
| `vantage_manager` | bool | true | the manager probes too |
| `vantage_groups` | list, ≤ 8 entries, each `FormatUUID` (new: a canonical lower-case UUID) | empty | monitoring groups whose agents probe |
| `agents_per_group` | integer 1–10 | 3 | agents chosen per group |
| `fail_policy` | enum `any`, `majority`, `all` | `any` | when the check as a whole is down |

Descriptor `Check` hook (composed with a plugin's own hook, e.g. snmp's):

- `vantage_manager` false with no `vantage_groups` → "a check needs at
  least one vantage point".
- `vantage_groups` duplicated → refused.
- `vantage_groups` non-empty together with a non-empty `secret` → "checks
  with a secret are probed only from the Pulsix server". (`snmp` always
  names a secret, so `snmp` effectively stays manager-only.)
- `agents_per_group` or `fail_policy` set without `vantage_groups` is
  accepted (harmless) — the SPA hides them until a group is chosen.

The checks service, on create and update, refuses a `vantage_groups` entry
that is not a monitoring group of the check's organization ("unknown
monitoring group"). `DeleteGroup` refuses a group named by any check
definition's `vantage_groups` with an in-use error naming the check keys
(the `orgsecrets.InUseError` shape; the API answers 409 like a secret in
use). Renaming a group changes nothing (ids are stored).

The descriptor goldens, the catalogue, the SPA fixture, schema and en/de
texts follow the "Adding a plugin" note in
`internal/agent/checks/plugins/registry.go`.

## Vantage identity

A vantage is `manager` or an agent, identified by its host's id (the
agent's identity is its host). Stored as text: `manager` or the host UUID
in canonical form.

## Assignment (who probes what)

The probe runner (the holder of advisory lock 8314) computes assignments on
its refresh (every 30 s), once for all agents, and stores them; agents and
any manager instance read them from the database.

- For every resolved synthetic check of every monitored host with
  `vantage_groups`, per group: candidates are the group's member hosts
  (the group's selector, as check bindings resolve it) whose agent is
  online (`agents.isOnline`, 90 s), reports `probes_supported`, and is not
  the target host. Pick the `agents_per_group` candidates with the highest
  rendezvous hash `sha256(target host id ‖ check key ‖ agent host id)`;
  an agent chosen by two groups counts once. Rendezvous hashing keeps
  assignments stable: an agent going offline moves only its own slots.
- An agent never gets more than **200** assignments; when a pick would
  exceed it, the next candidate by hash takes it (and the slot may end up
  with fewer agents than asked, logged once per refresh).
- Table `probe_assignments` (migration 00098, additive): `org_id`,
  `agent_host_id`, `target_host_id`, `check_key`, `plugin`, `params jsonb`
  (the check's params minus the four vantage params), `interval_seconds`,
  `address`, `assigned_at`; PK (`agent_host_id`, `target_host_id`,
  `check_key`); FKs to hosts with `ON DELETE CASCADE`; RLS like the other
  org tables. The runner writes each organization's set as a diff in one
  transaction per organization (insert new, update changed, delete gone).
- `host_agents.probes_supported boolean NOT NULL DEFAULT false` (same
  migration), written by the heartbeat when it changes.

## Agent protocol (`proto/pulsix/agent/v1/agent.proto`)

Additive fields and messages; an older agent never sees any of them.

- `Heartbeat.probes_supported = 7` (bool), `Heartbeat.probe_config_version
  = 8` (string; "" when it has none).
- `ManagerMessage.probe_config = 3` (optional `ProbeConfig { string
  version = 1; repeated ProbeSpec probes = 2; }`), `ProbeSpec {
  target_host_id, check_key, plugin, params_json (canonical),
  interval_seconds, address }`.
- `AgentMessage.probe_results = 4` (`ProbeResults { repeated ProbeResult
  results = 1; }`), `ProbeResult` = `CheckResult`'s fields plus
  `target_host_id`.
- Version = hex SHA-256 over the assignments' canonical encoding sorted by
  (target host id, check key); the empty set has the version of the empty
  encoding.
- The manager (`internal/agentapi`), on a heartbeat with
  `probes_supported`, reads the agent's assignments (one indexed query, no
  resolve), and rides a `ProbeConfig` on the ack when the version differs.
  An agent that does not report `probes_supported` gets none and its
  assignments are never made in the first place.
- Ingest of `ProbeResults`: each result must name a (target host, check
  key) currently assigned to this agent and a metric of the assignment's
  plugin; anything else is dropped and counted (no error to the agent,
  which may simply be one refresh behind). The same batch limits, budget
  and plausibility window as check results apply; results are stored as
  per-vantage observations (below), vantage = the agent's host id.

## Agent side (`internal/agent/probes`, new)

- Holds the current `ProbeConfig`; schedules each assignment every
  `interval_seconds` (first run spread over the interval by a hash of the
  slot); at most **4** probes at once; runs them with
  `internal/probe/all`'s registry (pollers not needed: `snmp` never reaches
  an agent) and a `probe.NewGuard(nil, nil)` guard — the same loopback,
  link-local and metadata refusals as the manager.
- Results go out as `ProbeResults` through the existing send path and
  replay ring (chunks of 200), `At` = the round's start, one `At` per
  round.
- A plugin the agent's registry does not know, or params it refuses, yield
  that assignment's metrics as errors (`all.FailAll`), never a crash.
- `probes_supported` is true on every platform the agent builds for; a
  probe that cannot run on a platform (e.g. unprivileged ICMP) reports its
  own errors as on the manager.

## Per-vantage storage

Migration 00098 also adds:

- `probe_current` — PK (`host_id`, `check_key`, `vantage`, `item`,
  `metric`), columns as `check_current` (`org_id`, `value`, `label`,
  `error`, `at`); FK (`org_id`, `host_id`) to hosts, cascade; RLS.
- `probe_samples` — hypertable on `at`, 1-day chunks, 90-day retention,
  columns as `check_samples` plus `vantage`; index (`org_id`, `host_id`,
  `check_key`, `vantage`, `item`, `metric`, `at DESC`).

Only checks with `vantage_groups` write here; a manager-only check writes
only its usual tuples. When the runner's refresh finds a vantage no longer
expected for a slot (group removed, agent reassigned), it deletes that
vantage's `probe_current` rows of the slot; samples stay until retention.

## Combining (`internal/probes`)

The probe runner is the only writer of a vantage check's combined result.
At each due time of a slot with `vantage_groups`:

1. When `vantage_manager` is true, it probes as today and stores the result
   as vantage `manager` in the per-vantage tables (not in the check's
   tuples).
2. It reads the slot's `probe_current` rows of the **expected** vantages
   (`manager` when enabled, plus the slot's current assignments).
3. Per vantage, the latest round is the rows with that vantage's greatest
   `at`; it is **fresh** when `at` is within 2 × interval + 30 s of now.
   A fresh vantage is **down** when `<plugin>.up` = 0, **up** when it is 1,
   otherwise it has **no result** (error or missing `up`).
4. With `n` vantages up or down and `d` of them down: down when `any` →
   `d ≥ 1`; `majority` → `d > n/2`; `all` → `d = n`. With `n = 0` the
   check reports `all.FailAll(plugin, params, "no vantage point has a
   recent result")` (unknown, as an unprobeable check today).
5. The combined observations are the latest round of one representative
   vantage, re-stamped with the combining time: when down, the first down
   vantage; when up, the first up vantage; order `manager`, then agents by
   host name, then host id. Recorded with `RecordProbeObservations`, so
   rules and alerts see them as today.

The combined result therefore follows agent results with at most one
interval's delay — the same cadence the manager's own probe has.

## API and SPA

- The host's checks response gains, per check with `vantage_groups`,
  `vantages`: `[{vantage, name, kind: manager|agent, status:
  up|down|none, at, observations: [{item, metric, value, label, error}]}]`
  in the combining order (`name` "Pulsix server" for the manager — the
  SPA translates it — and the host name for an agent), plus
  `fail_policy`.
- The samples endpoint used for graphs takes an optional `vantage` query
  parameter and then reads `probe_samples`; validation as for the other
  parameters.
- SPA checks dialog: the four parameters for synthetic plugins
  (`vantage_groups` as a multi-select of the organization's monitoring
  groups; `agents_per_group` and `fail_policy` shown once a group is
  chosen; a hint that checks with a secret are probed only from the
  server). Host page: below a vantage check, a table of its vantages
  (name, status badge, response time where the plugin has one, age) and a
  vantage selector on its graph. en/de texts.

## Tests

- Descriptor: params, hook (no vantage, duplicates, secret with groups,
  snmp), goldens; service: unknown group refused, group delete refused
  with keys.
- Assignment: rendezvous stability (an agent leaving moves only its
  slots), target excluded, offline and non-capable agents excluded, the
  200 cap, two groups sharing an agent, the diff write.
- Combining: every policy with up/down/none mixes, freshness boundary,
  `n = 0`, representative choice and order, manager disabled, a vantage
  no longer expected ignored and its rows deleted, manager-only checks
  unchanged (no per-vantage writes).
- agentapi: config rides the ack only on mismatch and only for capable
  agents; results for an unassigned slot or a foreign metric dropped;
  batch limits.
- Agent: scheduling, concurrency 4, guard refusals, unknown plugin →
  errors, results carry the target and one `At` per round.
- API/SPA: the `vantages` block, the samples `vantage` parameter, the
  dialog fields and the host-page table.

## Documentation

`docs/BACKLOG.md` (3.13a done; new rows for zone-worker vantages,
confirmation by re-probe, rules per vantage), `docs/upgrades.md`
(migration 00098; version skew: only agents of this release probe; an
older manager ignores the new heartbeat fields), noix-docs: synthetic
checks / check-plugins reference (vantage points, fail policy, the secret
rule, the agent's extra load and network position), changelog. No tech
stack named.
