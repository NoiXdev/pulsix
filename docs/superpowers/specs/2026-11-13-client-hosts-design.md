# Client hosts (backlog 3.57) — design

Status: the owner raised it on 2026-11-13 ("ein client ist ja mehr oder
weniger ein normaler PC oder Notebook, was schon mal über Tage und Stunden
aus sein kann, da sollte das Monitoring mit umgehen können") and accepted
the proposal ("ja gerne, wie du vorgeschlagen hast"): a host is a server or
a client; a client that is off is shown as off, raises no host alert and
keeps its alerts; once back it is judged normally; an optional warning
after a client has not been seen for some days. Every other decision below
is **(Claude's call)**.

## Intent

Today a host whose agent goes offline turns every tuple stale after three
intervals, the roll-up reads unknown, and a host alert opens with a
notification; tuple alerts close as `stale`. For a server that is right.
A workstation or notebook is off every evening and on weekends, so the
same logic produces an alert storm and loses its real alerts (a full disk
closes as stale overnight and reopens next morning).

## Data model

- `hosts.kind text NOT NULL DEFAULT 'server'` with a CHECK
  `kind IN ('server','client')` (additive migration, next free number).
  Every existing host stays a server; nothing changes for it.
- Host create/update (API and SPA host form) carry `kind`; the hosts list
  and host page show it. The host DTO gains `kind`.
- A client without an agent behaves exactly like a server (nothing tells
  the manager it is off); the form says so in a hint.

## Evaluation

Let *online* be the agents list's own definition (`agents.isOnline`:
`last_seen` within `agents.StaleAfter`). For a client host with an agent:

1. **Off (agent not online): the host is frozen.** The evaluation leaves
   every tuple state, the roll-up and every open alert exactly as they are:
   no transition to unknown, no host alert, no `stale` close, no
   notification. Synthetic (manager-probed) checks of the host are frozen
   too — a ping to a notebook that is off is expected to fail. Reported
   results that still arrive (buffered agent results on reconnect) are
   stored as always; they are judged once the host is no longer frozen.
2. **Back (agent online): judged normally**, with one change: the
   staleness deadline of a tuple is measured from
   `max(tuple reading, agent connected_since)` (the column from 3.12c)
   instead of from the reading alone, so a 24 h check that has not run
   since boot is not unknown the moment the agent reconnects. Every other
   rule (thresholds, hysteresis, flapping, maintenance) is unchanged.
3. **Absent warning (optional).** An organization setting
   `client_absent_days` (integer, 0 = off, default 14, max 365), stored in
   a new table `org_monitoring_settings (org_id PK, client_absent_days,
   updated_at)` with the same row-level-security policies as
   `org_job_settings` (00033); a missing row means the default. 3.15 will
   add its retention setting to the same table. When a
   frozen client's agent was last seen longer ago than that, one host
   alert opens with severity `warn` and the cause "not seen for N days".
   It is the host-scope alert subject (scope `host`), which until now only
   ever carries severity `unknown`; severity `warn` on a host-scope alert
   is the discriminator, and the alert list, the alert page and the
   notification texts say "not seen since …" for it instead of the
   unknown wording. No schema change to `alerts`. It closes as resolved when the agent is online
   again. While frozen, the host's next due time is the absent deadline
   (or the existing long silent heartbeat when the setting is off), so the
   ticker comes back exactly when the warning is due.
4. A server host, and a client without an agent, are unchanged.

Switching a host from client to server while its agent is off unfreezes
it at the next evaluation (it then goes unknown as any server would);
switching server to client while its agent is off freezes it and closes
an open unknown host alert as `stale` (the subject no longer applies, it
did not recover), without reopening anything. Changing the kind
marks the host dirty for evaluation.

## Display

- Hosts list, fleet overview and host page: an offline client shows an
  "Off" badge (de "Aus") with "last seen …" instead of its frozen roll-up
  colour; its last roll-up is shown muted. Online clients look like
  servers plus a small "Client" tag.
- The alert list shows frozen alerts unchanged (they are still true as of
  the last reading); the host page says the host is off.

## Item expiry and retention

- The item expiry (3.12c) is unchanged; a client off over night does not
  reset `connected_since` (24 h gap), one off over a weekend does, and
  automatic expiry then waits about a day, as after any outage.
- Status pages (5.x) will count only servers in availability; noted in
  the 5.5 row, nothing to build now.

## Tests

- Rules (pure Evaluate): frozen client keeps states/roll-up/alerts and
  produces no changes; online client with a long-interval tuple read
  before the reconnect is not stale until `connected_since` + 3×interval;
  server unchanged; client without agent unchanged; next due = absent
  deadline.
- Alerts: absent warning opens after `client_absent_days`, carries its own
  cause, closes resolved when online; setting 0 never opens it; no unknown
  host alert for a frozen client; server→client closes an open unknown
  host alert as stale.
- DB-backed Load: kind, agent last_seen and connected_since reach the
  snapshot; changing the kind marks the host dirty.
- API (apitest): host create/update/read with `kind`, invalid kind 422;
  org setting read/write with bounds.
- SPA: host form kind select with the no-agent hint; Off badge on list,
  fleet and host page; org setting field.

## Documentation

`docs/BACKLOG.md` (new row 3.57 done; 5.5 note), `docs/upgrades.md`
(migration, behaviour: nothing changes until a host is marked client),
noix-docs (hosts page: server/client; monitoring page: what a client
does when off; org settings: absent warning; changelog).

## Non-goals

- Selecting hosts by kind in a monitoring-group selector (later row if
  wanted; tags already do it: tag clients `role=client`).
- Detecting the kind automatically from facts.
- Per-group absent thresholds.
