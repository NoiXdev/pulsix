# Forgetting items that are gone (backlog 3.12b) — design

Status: the owner chose on 2026-11-12 to have both an automatic expiry and a
manual "forget" action. Every other decision below is marked **(Claude's
call)**.

## Intent

A check's item is one container, mount, interface or unit on a host. When it
disappears for good (a container removed, a disk unmounted), Pulsix keeps its
last values for ever: the host page shows them badged stale, rules on them go
unknown, and the rows count against the per-host tuple cap
(`checks.MaxTuplesPerHost` = 4096). On hosts that create many uniquely named
short-lived containers, the cap eventually refuses every new tuple of that
host, from every check, and nothing on the host page says so.

3.12b lets Pulsix forget such items: automatically after a quiet period, and
at once when an operator says so.

## Goals

- **Automatic expiry (owner's decision):** an item that has delivered no
  reading for **7 days** while its host kept reporting is forgotten.
- **Manual forget (owner's decision):** an operator can forget one item from
  the host page at once.
- Forgetting is not deleting history: samples (graphs) stay until the normal
  sample retention removes them.
- An item that reports again after being forgotten simply appears again,
  like a new item.

## What "forget" means

Forgetting the item `(host, check_key, item)` removes, for every metric of
that item:

- its current values (`check_current`);
- its rule states (`check_states`) and their state-change history used for
  flapping (`check_state_changes`), so a later reappearance starts fresh;
- its discovery row (`check_items`), so it leaves the rule-item suggestions
  and decision previews, and a reappearance is discovered again with a new
  `first_seen`. The whole-host item `""` has no such row. **(Claude's call,
  added after review:** otherwise a forgotten item stays in those lists for
  ever with its original `first_seen`, and on hosts with many short-lived
  containers the list keeps growing. Migration files are immutable, so
  00048's comment is not edited; the code that documents `check_items` says
  it now.)
- any open alert on it, which is closed with the new closed reason
  `forgotten` and an alert event saying so. Notification routes treat it like
  a resolve. **(Claude's call:** an open alert on something that no longer
  exists must not stay open for ever; "resolved" would claim the problem
  was fixed, "stale" means "data stopped", so a reason of its own is the
  honest one. The migration only widens the reason's CHECK constraint.)

Item decisions (ignore/watch patterns) are untouched: they are patterns, not
items.

Samples (`check_samples`) are untouched.

## Automatic expiry

- An item is due when its newest reading (max `at` over its metrics in
  `check_current`) is older than the expiry period **and** its host kept
  reporting after T = that newest reading plus the period, which holds when
  any of these does: (a) the item's own check key on that host has a reading
  (any metric, any item, value or error) newer than T — a sibling item such
  as another mount still reports; (b) the host has an agent whose last
  heartbeat (`host_agents.last_seen`) is newer than T — the check was
  removed, the agent is alive; (c) the host has no agent and any reading of
  it is newer than T. The manager's own checks (probes) write their results
  under the host they check, so on a host with an agent its newest reading
  says nothing about the agent: a host whose agent is offline keeps all its
  last values, however long it is offline, even while checks from the server
  keep running against it. A host that stopped reporting altogether keeps
  everything too. The selection and the re-check inside each forget apply
  the same rule. **(Claude's call, changed after the final review:** the
  first version counted any reading of the host, so a probe result kept an
  offline agent's host "reporting" and its agent's items were forgotten.)
- The whole-host item `""` follows the same rule (it covers a check that was
  deleted or unbound from the host: its tuples stop arriving while other
  checks continue).
- Period: 7 days by default; the instance setting
  `PULSIX_ITEM_EXPIRY` (a Go duration, at least `1h`; `0` disables the
  automatic expiry) changes it. **(Claude's call.)**
- It runs as a manager loop every hour, bounded per round (at most a few
  thousand items per round, in batches), under a session advisory lock so
  that exactly one manager runs it, the pattern of the retention and
  backup loops. A panic is recovered and logged. **(Claude's call.)**
- Each round logs how many items it forgot, per organization.
- Automatic forgets write no audit entry per item (there may be thousands);
  the log line is the record. **(Claude's call.)**

## Manual forget

- On the host page, every item row that is stale (or whose check no longer
  asks for it) offers **Forget**. It asks for confirmation ("Forget
  `<item>`? Its current values and open alerts are removed; its graphs stay.
  If it reports again it comes back.").
  **(Claude's call:** a fresh item is not offered the action — forgetting it
  would undo itself within one interval and only confuse.)
- API: `DELETE /api/v1/orgs/{org}/hosts/{host}/items` with `check_key` and
  `item` in the query (the item name may contain any characters, so not in
  the path). 204 on success, 404 when nothing matched. Role: the same role
  that may write item decisions. **(Claude's call.)**
- It writes an audit entry (host, check key, item, actor).
- The server does not refuse forgetting a fresh item; the GUI simply does not
  offer it. **(Claude's call:** the API stays simple and an automation can
  clean up as it likes.)

## Layers

- `internal/checks` (or a small new domain package next to it) owns
  `ForgetItem(ctx, org, host, checkKey, item, reason)` and the expiry
  selection; the store does the deletes in one transaction per item so a
  concurrent ingest either lands before (and is removed) or after (and
  re-creates the item).
- The alert closing goes through the alerts service, not raw SQL from the
  checks package, so its events and notification routing stay in one place.
- The loop lives in the manager wiring next to the other loops.

## Documentation

- `docs/BACKLOG.md`: 3.12b done.
- `docs/upgrades.md`: the new setting, the new closed reason, and that items
  quiet for 7 days are forgotten after the upgrade (the first round may
  forget many at once on long-running installations).
- End-user docs (noix-docs): the host page's Forget action, the automatic
  expiry and its setting, and the `forgotten` alert reason; remove the
  recommendation to use labeled mode because of the cap, keeping it only as
  a way to reduce noise.

## Non-goals

- A bulk "forget all stale items of this host" action.
- Changing sample retention (backlog 3.15).
- Forgetting whole hosts.

## Left to the builder

- The exact batch size and query shape of the expiry, within the bounds
  above.
- Whether the per-item forget and the expiry share one store statement.
- The GUI placement of the action within the existing item row.
