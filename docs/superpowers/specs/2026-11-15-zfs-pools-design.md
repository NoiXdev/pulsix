# ZFS pool health (backlog 3.9a) — design

Status: the owner accepted the proposal on 2026-11-15 ("passt weiter"): a
new agent plugin `zfs`, built like `raid`: one item per pool plus the
whole-host item `""` (pool count, host-level errors); data from the
`zpool` program (text output, since the JSON output needs OpenZFS 2.3 and
the current Debian/Ubuntu releases ship older ones), looked up in the
standard locations before `PATH`; Linux only; a 30 s limit per call; no
`zpool` installed → 0 pools and no error; an optional `pools` list; the
metrics below, every one sent on every run; no seed rules (the docs show
example rules); no migration, no new Go module; built against recorded
outputs, real-hardware validation left open. Not part of it: datasets and
quotas, snapshots, ARC statistics, starting a scrub. One change from the
proposal, **(Claude's call)**, stated to the owner with the report: a pool
that never finished a scrub reports `zfs.scrub_age_days` counted from the
pool's creation instead of an error "never scrubbed", because a collection
error makes the tuple, and so the host, unknown on every run until the
first scrub. Every other decision below is **(Claude's call)**.

## Descriptor (`internal/checkplugins/zfs.go`)

- `Name: "zfs"`, `Runs: Agent`, `Params: hardwareList("pools",
  FormatNoControl)`, `ItemParam: "pools"`, `HostItemAlwaysAsked: true`
  (the shape of `raid`).
- Metrics:

| Metric | Kind | Item | Value |
|---|---|---|---|
| `zfs.state` | discrete | pool | the pool's health: `ONLINE`, `DEGRADED`, `FAULTED`, `OFFLINE`, `UNAVAIL`, `REMOVED`, `SUSPENDED` (as `zpool` prints it) |
| `zfs.capacity_pct` | numeric | pool | allocated ÷ size × 100 (the `capacity` property) |
| `zfs.size_bytes` | numeric | pool | `size` |
| `zfs.free_bytes` | numeric | pool | `free` |
| `zfs.fragmentation_pct` | numeric | pool | `fragmentation`; absent when `zpool` prints `-` |
| `zfs.errors_read` / `zfs.errors_write` / `zfs.errors_checksum` | numeric | pool | sum of the column over every row of the pool's configuration tree |
| `zfs.data_errors` | numeric | pool | permanent data errors (`errors:` line) |
| `zfs.devices_unhealthy` | numeric | pool | leaf devices whose state is not `ONLINE` |
| `zfs.scan` | discrete | pool | `none`, `scrub`, `resilver`, `scrub_paused` |
| `zfs.scan_pct` | numeric | pool | progress of the running scrub or resilver; 100 when none runs |
| `zfs.scrub_age_days` | numeric | pool | days (fractional) since the last scrub finished; since the pool's creation when none ever did |
| `zfs.pools` | numeric | `""` | pools reported this round, after the `pools` filter, also 0 |

## Agent plugin (`internal/agent/checks/plugins/zfs/`)

### Sources

- **Gate:** `/dev/zfs` missing → no ZFS on this host: `zfs.pools = 0`, no
  error, no command run (as `raid` without `/proc/mdstat`). `zpool` not
  found (with `/dev/zfs` present) → 0 pools, no error.
- **Lookup:** `zpool` and `zfs` in `/usr/sbin`, `/sbin`, `/usr/local/sbin`
  (the first that exists and is executable), else on `PATH` — the order
  of `smart`'s locator. Executed directly, never through a shell, with a
  fixed argument list, the agent's environment plus `LC_ALL=C` (dates and
  numbers in one format), stdout capped at 1 MiB (more → that call's
  error), 30 s per call, at most 60 s for one collection.
- **Calls, in order:**
  1. `zpool list -Hp -o name,health,size,free,capacity,fragmentation` —
     tab-separated, exact numbers. `capacity` and `fragmentation` may
     carry a `%` (older releases) or be `-`.
  2. `zpool status -p` — every pool (the filter is applied after
     parsing), per pool: the `scan:` paragraph, the `config:` tree, the
     `errors:` line.
  3. Only when at least one reported pool never finished a scrub:
     `zfs get -Hp -o name,value creation <pool>…` (the pools' root
     datasets; a Unix time).
- A failing call 1 (exit status, timeout, oversized output) → one
  collection error on item `""`, metric `zfs.pools`, nothing else (the
  pools' values turn stale, as when the agent cannot run). A failing call
  2 → the `zpool list` metrics stand and every status-derived metric of
  every pool is a collection error naming the problem. A failing call 3
  → `zfs.scrub_age_days` of the never-scrubbed pools is a collection
  error. Error texts quote at most the first line of stderr, cleaned and
  cut to 256 bytes.

### Parsing `zpool status -p`

- Pools start at `pool: <name>`; a pool missing from `zpool list` (it
  vanished between the calls) is ignored; a pool in `list` but missing
  from `status` gets the status-derived metrics as collection errors
  ("missing from zpool status").
- **scan:** (the line and its continuation lines, up to `config:`)
  - `scrub in progress since …` with a later `… N% done` (or computed
    from `X scanned … of Y` / `X issued … of Y` when no percentage is
    printed) → `scrub`, that percentage.
  - `resilver in progress since …` → `resilver`, its percentage.
  - `scrub paused since …` → `scrub_paused`, the percentage when
    printed, else 0.
  - `scrub repaired … on <date>` → `none`, 100; last scrub end = date.
  - `scrub canceled on <date>` → `none`, 100; the last *finished* scrub
    is unknown from this line, so the age counts from creation (a
    canceled scrub is not a finished one).
  - `resilvered … on <date>` (a finished resilver) → `none`, 100; a
    resilver is not a scrub: age from creation unless a scrub line also
    names one (newer releases print both on separate lines).
  - `none requested` or no `scan:` line → `none`, 100; age from creation.
  - A percentage is clamped to 0–100; one that does not parse makes
    `zfs.scan_pct` a collection error and `zfs.scan` still reports the
    kind.
  - Dates are `LC_ALL=C` ctime form (`Sun Nov 10 00:24:02 2024`), in the
    host's local time zone; one that does not parse makes
    `zfs.scrub_age_days` a collection error. An end date in the future
    (clock moved) reads 0.
- **config:** the tree's rows `NAME STATE READ WRITE CKSUM [notes]`,
  indentation giving the depth. Error counters: the sum of READ, WRITE
  and CKSUM over every row that has them — the pool row, vdevs (mirror,
  raidz, draid), leaves, and the `logs`, `cache`, `special`, `dedup`
  groups' rows — since each row counts its own errors, not its
  children's. `-p` prints exact counts; a counter that is not an
  integer (an older release's `1.2K`) is parsed with its suffix
  (K/M/G/T, ×1000 each, as `zpool` abbreviates). The `spares` group's
  rows carry no counters and are skipped for the sums.
- **devices_unhealthy:** leaves (rows with no deeper row beneath them)
  whose state is not `ONLINE`, outside the `spares` group (`AVAIL`,
  `INUSE`) — a spare that stands in appears inside the vdev it replaces
  (under `spare-N`) and counts by its own state there.
- **errors:** `No known data errors` → 0; `<N> data errors, use '-v' for
  a list` → N; anything else → collection error on `zfs.data_errors`.
- **State** comes from `zpool list`'s health column (`zfs.state` stands
  even when `status` fails).

### Plugin

- Items: a pool name that is not `checks.ReportableItem` is skipped and
  named in the advisory error, as `raid` does; the `pools` filter as
  `raid`'s `arrays` (a listed pool that does not exist simply reports
  nothing). Every metric each run for every reported pool, except
  `zfs.fragmentation_pct` when `zpool` prints `-` (then never present,
  so never stale).
- Off Linux: one collection error on item `""`, metric `zfs.pools`: "not
  supported on this platform" (a stub source like `raid`'s).
- Seams for tests: a command runner interface (name, args → stdout,
  stderr, error) and the `/dev/zfs` check and clock, injected; no test
  runs the real `zpool`.

## Manager and SPA

The descriptor drives validation, items, rules catalogue and API enums.
Everything else from the "Adding a plugin" note in
`internal/agent/checks/plugins/registry.go`: the literal golden tests, the
`checks.PluginZFS` constant, the SPA fixture and schema regeneration, en/de
plugin names in both picker maps and the `pools` parameter texts. No seed
rules, no migration.

## Tests

- Parser table tests with recorded outputs under `testdata/`: a healthy
  mirror; raidz2 DEGRADED with a FAULTED leaf; a FAULTED pool; a resilver
  in progress (with a `spare-0` standing in); a scrub in progress; a
  scrub paused; a scrub canceled; never scrubbed; a finished resilver
  only; a pool with `logs`, `cache`, `special` and `spares`; non-zero
  READ/WRITE/CKSUM at vdev and leaf level; `N data errors`; two pools;
  older-release forms (`%` suffixes, abbreviated counters, `X scanned …
  of Y` without a percentage).
- Plugin tests with a fake runner: no `/dev/zfs`; no `zpool`; each call
  failing (exit status, timeout via a blocking fake, oversized output);
  the filter; an unreportable pool name; a pool vanished between the
  calls; scrub age from the end date and from creation (fixed clock);
  `zfs get` only called when needed; no shell, `LC_ALL=C` passed; the
  stub off Linux.
- Descriptor goldens as the note lists; SPA fixture and texts.

## Documentation

`docs/BACKLOG.md` (3.9a done; a new row for real-hardware validation of
`zfs`), `docs/upgrades.md` (version skew: an older agent does not know
the plugin), noix-docs: check-plugins reference (the `zfs` plugin, its
parameter, metrics, platform note, example rules: pool not `ONLINE`,
`zfs.data_errors` above 0, `zfs.scrub_age_days` above 35), changelog. No
tech stack named.

## Non-goals

Datasets, quotas, snapshots, ARC statistics, starting or stopping
scrubs, per-device items, pools on non-Linux hosts.
