# Synthetic checks (backlog 3.13) — design

Status: the owner asked on 2026-11-11 to build 3.13 and handed the open
decisions to Claude ("leg los"). Every decision below that the owner did not
make is marked **(Claude's call)**.

## Intent

Pulsix watches a host from the inside through its agent. 3.13 adds the view
from the outside: is the host reachable, does the port answer, does the web
page come back with the right status, is the certificate about to expire, does
the name resolve. These checks need no agent on the target, so a host with
`monitored` set and no agent (a switch, an appliance, a website) becomes
monitorable at all. A host that has an agent gets them as well.

## Goals

- Five synthetic check plugins: `icmp`, `tcp` (with an optional banner
  match), `http` (HTTP and HTTPS), `tls`, `dns`.
- They are check definitions like every other plugin: same bindings (org,
  monitoring group, host), same precedence, same intervals, same GUI, and
  their results land in the same current values and samples, so rules,
  alerts, graphs and the fleet view work on them unchanged.
- They run from **one vantage point, the manager itself** (the
  architecture spec's built-in probe, §9.3). **(Claude's call:** every
  installation has a manager, while zone workers are optional and have no
  monitoring loop today. Several vantage points, the fail policy and
  confirmation by a second probe are backlog 3.13a and build on this.)
- A failed probe is a **result, not a gap**. A connection that is refused or
  times out reports `*.up = 0`, so a rule can raise an alert. Only a check
  that could not be attempted at all (no address, a blocked target) reports a
  collection error, which reads as unknown.

## Non-goals

- `snmp`: split into a new backlog row, 3.13b. SNMP needs a community string
  or v3 credentials, which need the secret references that 3.5/3.6 have not
  shipped, plus a new dependency. **(Claude's call.)**
- HTTP headers, request bodies, authentication, JSONPath extraction and
  per-assertion severities (architecture spec §9.4, backlog 3.6h).
- Several probes per check, per-probe results, fail policy (3.13a).
- Probing from agents or workers.
- Seeding synthetic checks into organizations. A new installation does not
  start probing every host from the manager unasked. **(Claude's call:**
  ICMP may not be permitted in the manager's container, and every host
  would turn unknown.)

## Plugins

Each plugin probes one target per host. The target is the host's `address`
unless a parameter names one. In the parameters `url` and `name`, the text
`{address}` is replaced with the host's address, so one definition bound to
the org or a group works for every host in it. **(Claude's call.)**

A host whose address is empty, where the check needs it, reports one
collection error per metric: "host has no address".

Every `*_ms` metric is a duration in milliseconds, measured on the manager.
A metric reported "only when up" is absent from a failed attempt; it is not
reported as an error.

| Plugin | Parameters | Metrics (item is always `""`) |
|---|---|---|
| `icmp` | `count` (1–10, default 3), `timeout_s` (1–30, default 5) | `icmp.up` (1 when at least one reply arrived), `icmp.loss_pct` (0–100), `icmp.rtt_ms` (average over the replies, only when up) |
| `tcp` | `port` (1–65535, required), `send` (optional text written after connecting), `expect` (optional substring looked for in the first 4 KiB read within the timeout), `timeout_s` (1–30, default 5) | `tcp.up`, `tcp.connect_ms` (only when up), `tcp.banner_match` (1/0, only when `expect` is set and the connection was up) |
| `http` | `url` (required, `http://` or `https://`, may contain `{address}`), `method` (`GET` or `HEAD`, default `GET`), `follow_redirects` (default true, at most 10), `verify_tls` (default true), `body_contains` (optional substring looked for in the first 1 MiB of the body), `timeout_s` (1–60, default 10) | `http.up` (1 when a response arrived), `http.status_code`, `http.response_ms` (time to the end of the body read), `http.body_match` (1/0, only when `body_contains` is set and up) |
| `tls` | `port` (default 443), `server_name` (default: the address's host part), `timeout_s` (1–30, default 10) | `tls.up` (1 when the handshake completed, verified or not), `tls.handshake_ms`, `tls.cert_valid` (1 when the chain verifies against the system roots and matches `server_name`, else 0), `tls.cert_expiry_days` (days until the leaf's NotAfter, fractional, negative once expired) |
| `dns` | `name` (default `{address}`), `type` (`A`, `AAAA`, `CNAME`, `MX`, `TXT`, `NS`; default `A`), `server` (optional `ip` or `ip:port`; default the manager's resolver), `expect` (optional: up only counts as matched when one answer equals it, compared case-insensitively without a trailing dot), `timeout_s` (1–30, default 5) | `dns.up` (1 when the query was answered with at least one record), `dns.resolve_ms`, `dns.answer_match` (1/0, only when `expect` is set and up) |

`tls.up` counts a certificate that does not verify as up (`tls.cert_valid`
says whether it verifies). `http` with `verify_tls` true fails the request on
such a certificate, which reads `http.up = 0` like any failed attempt. A failed
attempt stores no reason: an observation is a value or an error, and an error
reads unknown, not down. **(Claude's call;** a reason metric is a follow-up.)

The SPA marks these five as "runs from the Pulsix server" in the plugin
picker, so nobody expects them to run on the host.

## Where they run

- **The resolver splits by location.** Host-local plugins (cpu, memory, disk,
  service, network, uptime) keep going to the host's agent. Synthetic
  plugins are never pushed to an agent: an agent does not know them and
  would report "unknown plugin". The version stamp that the agent compares
  covers only what it receives, so adding a synthetic check does not
  re-push the agent's set.
- **The manager's probe runner** runs the synthetic set of every monitored
  host in every organization. It resolves through the same resolver, so
  precedence, disabling and group membership behave exactly as for agent
  checks.
- **Exactly one manager probes** at a time, even with several managers
  (backlog 6.1 will run them side by side). It is the holder of a
  session-scoped advisory lock, the pattern of `schedule.NewAdvisoryLocker`
  that the retest and backup loops use. The holder never probes a check
  twice within one interval. A new holder, after a restart or a failover,
  may probe each check once early. **(Claude's call:** it needs no
  schedule table.)
- **Bounds:**
  - A tick never runs longer than its own deadline.
  - At most 32 probes run at once per manager.
  - A probe's timeout is capped below its check's interval.
  - A definition change (create, edit, disable, delete, host address or
    monitored flag) takes effect within one minute.
- **Results** are written through `checks.Service.RecordObservations` with
  the target host's id on every observation. That marks the host dirty for
  the rules ticker exactly as agent ingest does.

## Network safety

The manager is a shared service: an organization's operator must not use it
to reach what sits next to it. **(Claude's call.)**

- Every connection a probe makes is checked **at dial time**, against the
  resolved IP. That covers DNS answers, redirects and rebinding.
- Refused always: loopback, link-local (including `169.254.169.254`),
  unspecified, multicast.
- An instance setting, the environment variable `PULSIX_PROBE_DENY_CIDRS`
  (a comma-separated CIDR list), adds more. The compose README recommends
  adding the stack's own network.
- Private ranges are allowed by default, because watching the LAN is the
  point.
- A refused target is a collection error ("target address not allowed"),
  never `up = 0`, so it cannot be used to tell open ports from closed ones.
- No probe returns response content: only the metrics above, and collection
  errors cut to the existing error length limit.

## ICMP

ICMP uses unprivileged datagram ICMP sockets, so the manager needs no
`CAP_NET_RAW`. Where the kernel does not permit them (Linux's
`net.ipv4.ping_group_range`), `icmp` reports a collection error that says so.
`docs/upgrades.md` and the compose README name the sysctl. **(Claude's call;**
Docker's default sysctls permit it.)

## GUI and rules

- The check dialog and the shared check form offer the five plugins, each
  with fields for its parameters, validated like on the server.
- The rules catalogue gets an entry for every new metric, so rules can be
  built on them. For example: `http.up == 0` critical,
  `tls.cert_expiry_days < 14` warning.
- The host page and the graphs show these results like any other metric.

## Validation

Server-side validation mirrors the table:
- unknown parameters are refused;
- ranges and enums are enforced;
- `url` must parse with an http(s) scheme after `{address}` is substituted
  with a placeholder;
- `server` must be an IP with an optional port.

The API enums and the SPA's plugin lists gain the five names.

## Documentation

- `docs/BACKLOG.md`: 3.13 is done; new rows 3.13b (SNMP) and, if useful, any
  limits found while building.
- `docs/upgrades.md`: the new plugins, `PULSIX_PROBE_DENY_CIDRS`, the ICMP
  sysctl. Agents need no update.
- End-user docs (noix-docs, `reference/check-plugins.md`,
  `user/monitoring.md`): a "Checks from the Pulsix server" section. As
  always, it names no tech stack.

## Left to the builder

- The scheduling mechanism within the bounds above.
- The package layout. Suggested: `internal/probe` for the five probe
  functions (pure, with injectable dialer and clock), and a manager-side
  runner next to `internal/checks`.
- How tests reach real sockets: local listeners for tcp, http and tls,
  and a stub DNS server on loopback. In tests the deny list is
  injectable, so loopback can be allowed.
