# Check correctness bundle (backlog 3.10c, 3.12c, 3.55, 3.13c) — design

Status: the owner accepted the proposal on 2026-11-13 ("ja los"): advisory
errors reach the agent log and the names a plugin leaves out become count
metrics; an agent counts as a sign of life for the item expiry only once it
has been connected for `checks.MaxInterval`; tuples of items a check no
longer asks for leave the evaluation; vanished conditional hardware
metrics report an error instead of going stale silently; `smart.devices`
says "no device with SMART data" only when a device answered without data;
the agent's configuration version is stored and shown. Every other decision
below is **(Claude's call)**.

## 1. Advisory errors (3.10c)

A plugin's `Collect` may return observations **and** an error. The
scheduler (`internal/agent/checks/secret.go` `collect`) drops that error,
so what it says reaches nobody: `postgres` names the databases beyond its
256 cap and the names the item contract refuses, `docker` its refused
container names, several plugins join per-item failures.

- **Agent log.** For a plugin that is not a `SecretPlugin`, the scheduler
  logs a non-nil advisory error at warn level with the check key, once
  when its text changes for that key and again at most once an hour while
  it stays the same (state per check key, injected clock). A nil error
  after a logged one clears the state (no "resolved" line). Advisory
  errors of a `SecretPlugin` are never logged: they may carry driver text,
  and the repository's rule is that error texts never carry driver or
  server messages. A plugin panic or an observation-less round is
  unchanged.
- **Count metrics.** Where a plugin leaves out names deliberately, it
  reports how many on item `""`, every run (0 when none):
  `pg.databases_unreported` (databases beyond the cap plus refused names)
  and `docker.containers_unreported` (refused container names). Both get
  catalogue descriptors (`internal/checkplugins`), unit "count", so a rule
  can alert on them; no seeded rule. Other plugins' joined per-item
  failures already arrive as error observations on their items and need
  nothing new.

## 2. Item expiry after an agent outage (3.12c)

The expiry (`internal/checks/forget.go` `lastSignOfLife`, SQL
`ListExpiredItems`) treats an agent's `last_seen` as proof the host kept
reporting. After an outage longer than `PULSIX_ITEM_EXPIRY` the agent
heartbeats at once, before a check with a long interval (up to 24 h) has
run again, so its items are forgotten (alerts closed as `forgotten`) and
rediscovered a little later.

- New nullable column `host_agents.connected_since timestamptz` (additive
  migration, next free number). Every write that sets `last_seen`
  (`TouchAgentBySerial`, `RecordAgentHeartbeat`) also sets
  `connected_since = now()` when the previous `last_seen` is NULL or older
  than a reconnect gap, or `connected_since` is NULL; otherwise it keeps
  it. `UpsertHostAgent` (re-enrollment) resets it to NULL with
  `last_seen`. The gap is 30 minutes: three times the longest heartbeat
  interval an agent accepts (10 min), so a slow but steady agent never
  resets it; a named Go constant passed as a query argument.
- The agent counts as a sign of life only when
  `last_seen >= connected_since + checks.MaxInterval`; otherwise the
  agent contributes nothing (the item's own check key's newest reading
  still counts). Same rule in SQL (`ListExpiredItems`, the forget
  re-check query) and Go (`ItemNewest`, `itemExpired`).
- Upgrade: rows have NULL `connected_since` until the first heartbeat
  after the upgrade, so automatic expiry of agent items pauses for 24 h
  after an upgrade. `docs/upgrades.md` says so.

## 3. Tuples nobody asks for (3.55)

1. **Not asked → not judged.** The host page hides values of items a
   check's list no longer contains (`checks.Definition.AsksForItem`), but
   the rules evaluator judges them, so a mount taken off a disk check goes
   unknown and keeps the host unknown with an alert whose cause is not
   shown. `rules.CheckContext` gains, per check key, whether an item is
   asked (filled by the `internal/app` adapter from the resolved checks'
   plugin and params via `checkplugins.AsksForItem`; item `""` is always
   asked; a key without a resolved check stays as today). `Load` gives a
   tuple of an item not asked interval 0, the existing "no resolved
   interval" path: out of the evaluation, no state, open alerts closed the
   way that path closes them. The current row is not deleted; the item
   expiry forgets it later.
2. **Vanished conditional metrics.** `temp.high_celsius` /
   `temp.critical_celsius` (a limit that stops being stated) and the
   per-field `smart.*` metrics (a field that disappears or changes type)
   can strand while their item keeps reporting. The `temperature` and
   `smart` plugins become stateful (`checks.StatefulPlugin`, one instance
   per check key) and share a small helper in `internal/agent/checks`
   that remembers, per item, the metrics it reported with a value. In a
   round where the item reports at least one value, every remembered
   metric missing from it gets an error observation "<metric> is no
   longer reported". The helper forgets a metric once it has been missing
   for `MaxInterval` (24 h): from then on the tuple ages like any other,
   and the host page's Forget removes it for good. An item that reports
   nothing (or only errors) this round is left alone. The helper keeps no
   state beyond the agent process.
3. **`smart.devices`.** "no device with SMART data" is raised only when at
   least one device answered without SMART data and none sleeps — not in a
   round where every device failed (those failures are on the items).
4. **Validation list.** BACKLOG 3.9's real-hardware list gains "a SAS disk
   in standby" (whether smartctl prints no `smart_support` block for it).

## 4. Configuration version per agent (3.13c)

The agent reports the configuration version it runs on every heartbeat
(`Heartbeat.config_version`), and the manager compares it with the
resolved one (`internal/agentapi/stream.go` `processHeartbeat`), but
stores neither.

- New nullable columns on `host_agents`: `config_version text` (what the
  agent last reported), `config_expected text` (what the manager resolved
  at that comparison), `config_checked_at timestamptz` (additive
  migration). Written after each comparison processHeartbeat makes, in one
  UPDATE that only changes the row when one of the two versions changed
  or the stored check is older than 10 minutes (keeps the write rate
  low). A heartbeat inside the resolve throttle or with a failed resolve
  writes nothing. Re-enrollment resets the three to NULL.
- API: the agents list DTO and the host's agent DTO gain
  `config_version`, `config_expected`, `config_checked_at` (omitted when
  NULL). `schema.d.ts` regenerates.
- SPA: the agents list and the host page show a badge "Config current"
  when the two versions are equal, "Config outdated" otherwise (tooltip or
  detail with both versions, shortened), nothing when not yet known. i18n
  en/de.

## Tests

- Scheduler: advisory logged once, repeated after an hour, re-logged on a
  text change, never for a secret plugin (fake clock, captured slog).
- postgres/docker: the count metric with 0, with names over the cap /
  refused names.
- Expiry (SQL and Go, DB-backed): an agent back after a long outage does
  not expire a long-interval check's item until connected for
  `MaxInterval`; a steady agent still expires a vanished item; re-enroll
  resets; the gap boundary.
- Rules (DB-backed Load + adapter unit test): an item taken off a disk
  check's `mounts` leaves the evaluation and its open alert closes; item
  `""` and items still asked are judged as before.
- temperature/smart: a limit that stops being stated gets the error once
  per round while missing, stops after 24 h; a failing round adds nothing;
  `smart.devices` text only with an answering device without data.
- Config version: store write rules (changed, unchanged inside 10 min,
  throttled), API DTO fields through apitest, SPA badges.

## Documentation

`docs/BACKLOG.md` (3.10c, 3.12c, 3.55, 3.13c done; 3.9 list),
`docs/upgrades.md` (two migrations, expiry pause after upgrade, new
metrics), noix-docs (check plugin reference: the two count metrics, the
vanished-metric error, `smart.devices` text; monitoring page: item expiry
after an outage, items off a check's list; agents page: config badge;
changelog).

## Non-goals

- A generic vanished-metric mechanism for every plugin.
- Surfacing advisory errors in the GUI.
- Re-pushing configuration from the GUI.
