# Hardware checks (backlog 3.9) — design

Status: the owner asked on 2026-11-12 to build 3.9 and skipped the questions
("leg los"). Every decision below is **(Claude's call)** unless it restates
the backlog row ("SMART, temperatures, RAID (mdadm)").

## Intent

Disks die, fans stop and RAID arrays lose a member quietly. The host checks
Pulsix has today see none of it until a file system fills or a service stops.
3.9 adds three agent plugins that report the hardware's own warnings, so a
rule can alert before data is lost: a disk that starts reallocating sectors,
a temperature that climbs, an array that runs degraded.

## Goals

- Three new agent check plugins, each in its own package under
  `internal/agent/checks/plugins/` (the 3.52 layout), configured and bound
  like every other plugin: `temperature`, `smart`, `raid`.
- Results land in current values and samples, so rules, alerts, graphs and
  the fleet view work unchanged.
- A host that lacks the hardware or the tool reports one clear collection
  error per check, never a crash and never silence.

## Non-goals

- Hardware RAID controllers (MegaRAID, Smart Array, …), ZFS and btrfs pool
  health. ZFS pools are a new backlog row **3.9a**; vendor controllers stay
  out.
- IPMI / BMC sensors (fans, PSUs), GPU temperatures.
- Seeding these checks into organizations: most virtual machines have no
  SMART, no hwmon sensors and no md arrays, and a seeded check would show a
  collection error on each of them.
- Installing smartmontools for the operator.

## Plugin `temperature`

- Reads the operating system's temperature sensors (Linux hwmon and thermal
  zones, macOS SMC, Windows' ACPI thermal zones) through the library the
  agent already uses for host metrics.
- One item per sensor, named by the sensor key the platform reports.
- Metrics: `temp.celsius`; `temp.high_celsius` and `temp.critical_celsius`
  only when the sensor states such a limit (a non-zero value).
- Parameter: `sensors` (optional list): report only these sensor keys; empty
  means all.
- `temp.sensors` on item `""`: the number of sensors reported this round,
  after the filter, sent on every run that reported at least one.
  **(final review ruling)**
- A platform or host with no readable sensor (or a `sensors` list that
  matches none) reports a collection error "no temperature sensors found" on
  item `""`, metric `temp.sensors` — so the next good run replaces the error
  with the count. **(final review ruling; it was `temp.celsius`, which no
  good run sends for item `""`, so the error stood for ever)**

## Plugin `smart`

- Runs smartmontools' `smartctl` with JSON output. The agent never builds a
  shell command: it executes the binary directly with a fixed argument list,
  and device names come only from `smartctl --scan --json` or from the
  `devices` parameter, which the manager validates as device paths.
- `smartctl` is looked up in the standard install locations first
  (`/usr/sbin`, `/usr/local/sbin`, `/opt/homebrew/sbin`, `/opt/homebrew/bin`,
  `/usr/local/bin`), and on the agent's PATH only as the fallback. On
  Windows the agent uses only `%ProgramFiles%\smartmontools\bin\smartctl.exe`
  and never searches PATH: it runs smartctl as SYSTEM, and a folder on the
  machine-wide PATH may be writable by users who are not administrators.
  **(final review ruling)** The path found must be absolute, and on Windows
  an `.exe` (never a `.bat`, `.cmd` or `.com`). **(Claude's call, added
  after review)** When it is missing, the check reports one collection
  error on item `""`, metric `smart.devices`, that says to install
  smartmontools.
- It passes `-n standby,0` so that a sleeping disk is not spun up: smartctl
  then exits 0 without data for it, so a disk in standby reports nothing
  that round. Its values turn stale and, like every value the agent stops
  sending, read unknown after three intervals (the host turns unknown with
  a host-scope alert) for as long as the disk sleeps; after the item-expiry
  period (`PULSIX_ITEM_EXPIRY`, seven days by default, backlog 3.12b) a disk
  that never wakes is forgotten together with its open alerts. Operators
  with disks that spin down list only the always-on disks in `devices`, or
  ignore the sleeping ones; an opt-in wake or a standby-aware state is
  backlog **3.9b**. **(final review ruling)**
- Standby and "no SMART" are told apart without reading smartctl's message
  text: with `-n standby,0` smartctl stops before it identifies a sleeping
  disk, so that JSON has no `smart_support` block, while a device it did
  identify (a virtual disk, a USB bridge without SMART pass-through) has one,
  typically `{"available": false}`. **(final review ruling)**
- One item per device, named by its path (`/dev/sda`, `/dev/nvme0`, …). A
  disk behind a RAID controller — a scanned `-d` type whose family (the text
  before the first comma) is `megaraid`, `sat+megaraid`, `cciss`, `3ware`,
  `areca`, `aacraid`, `sssraid` or `hpt` — shares its path with its
  siblings and is named `<path> [<type>]` (`/dev/bus/0 [megaraid,1]`),
  whether or not it has siblings, so its item does not change when one
  comes or goes. **(Claude's call, added after review)**
- Metrics (each only when the device reports it):
  - `smart.passed` — 1 when the overall health self-assessment passed, else 0;
  - `smart.temperature_celsius`;
  - `smart.power_on_hours`;
  - ATA: `smart.reallocated_sectors` (attribute 5), `smart.pending_sectors`
    (197), `smart.offline_uncorrectable` (198) — raw values;
  - NVMe: `smart.nvme_percentage_used`, `smart.nvme_available_spare_pct`,
    `smart.nvme_media_errors`.
- `smart.devices` on item `""`: the number of devices found by the scan (or
  configured) and not refused, sent on every run that got as far as querying
  them. Every failure that concerns no single device is an error on this
  metric instead, so the next good run replaces it: smartctl missing, the
  scan failing or finding nothing, every device refused, and **no device
  with SMART data** — every device answered without readings and none of
  them is asleep (a virtual machine's disks, a USB stick), or every device
  failed. In that last case nothing else is reported apart from each failed
  device's own error, so such a host is never silent. **(final review
  ruling)**
- Parameter: `devices` (optional list of device paths; empty means every
  device `--scan` finds). A list replaces the scan: the devices are queried
  as given, without a `-d` type, so a disk behind a RAID controller cannot
  be addressed through it.
- Bounds: at most 4 devices queried at once, 20 s per device, the whole
  collection within 60 s; output read is capped (1 MiB per device).
- smartctl's exit status is a bit mask; bits that only report SMART findings
  (disk failing, attributes past threshold, error log entries) are data, not
  collection errors — the plugin still parses the JSON. Only "command line
  did not parse", "device open failed" and a missing or unparsable JSON are
  collection errors for that device.

## Plugin `raid`

- Linux software RAID only: reads `/proc/mdstat`. Other platforms report
  "not supported on this platform", like the service plugin's stub.
- One item per array (`md0`, `md127`, …).
- Metrics:
  - `raid.state` (label): `clean`, `degraded`, `resyncing`, `recovering`,
    `checking`, `reshaping`, `inactive`;
  - `raid.devices_total`, `raid.devices_active`, `raid.devices_failed`;
  - `raid.sync_pct` — on every run for every array: the running resync,
    recovery, check, repair or reshape's progress; 0 while one is queued
    (`PENDING`/`DELAYED`); 100 when none runs. **(final review ruling; sent
    only during a sync, the value went stale after the first one ended and
    left the host unknown for good)**
- `recovering` means degraded and rebuilding — a member is being rebuilt,
  for example after a hot spare took over a failed disk; a running or queued
  sync names the state before `degraded` does.
- Parameter: `arrays` (optional list; empty means every array found).
- No array at all is not an error: the check reports
  `raid.arrays = 0` on item `""` (and that metric with the count whenever it
  runs), so a rule can still notice an array that vanished.

## Manager, rules, GUI

- The manager accepts the three plugins with their parameters (unknown
  parameters refused; list entries non-empty, at most 64 entries, each at
  most 255 bytes; `devices` entries must start with `/dev/` on every platform
  — smartctl names Windows disks `/dev/sda`, `/dev/pd0`, `/dev/csmi0,1` too —
  contain only letters, digits and `. _ / , : + @ -`, and have no `..` path
  segment). **(amended after review: the `\\.\…` forms are refused)**
- A check with a list asks only for the items on it (backlog 3.28,
  `Definition.AsksForItem`): `sensors`, `devices` and `arrays` bound the
  items like `mounts` does, but the whole-host item `""` (`temp.sensors`,
  `smart.devices`, `raid.arrays` and the host-level errors) is always asked,
  and a smart item `<path> [<type>]` counts as asked when its `<path>` is
  listed. **(final review ruling)**
- The rules catalogue gets every metric above (check keys `temperature`,
  `smart`, `raid`); `raid.state` is discrete.
- The SPA offers the three plugins with their list parameters (the same
  multi-line list field the disk plugin uses), marks `raid.state` as a label
  metric (not graphable), and the docs give example rules:
  `smart.passed == 0`, `smart.reallocated_sectors > 0`,
  `raid.state == "degraded"`, `raid.devices_failed > 0`, `temp.celsius > 85`.

## Older agents and versions

An agent from before 3.9 skips an unknown plugin; the check shows no
readings until the agent is updated (the same wording as 3.12's).

## Testing

- Each plugin behind a provider seam with fixtures: hwmon-style sensor
  lists, recorded `smartctl --json` outputs for an ATA disk, an NVMe disk, a
  failing disk, a USB bridge without SMART, a disk in standby, and
  `/proc/mdstat` samples (clean, degraded, recovering with progress, a
  resync pending, inactive, raid0/1/5/6/10).
- No test runs the real `smartctl` or reads real hardware.

## Documentation

- `docs/BACKLOG.md`: 3.9 done; new rows 3.9a (ZFS pool health) and 3.9b
  (smart: opt-in waking or a standby-aware state for disks that spin down).
- `docs/upgrades.md`: the new plugins; agents need an update; smartmontools
  must be installed for `smart`.
- End-user docs (noix-docs): the three plugins, their parameters and metrics,
  the smartmontools requirement per platform, example rules. No tech stack
  named.

## Left to the builder

- The exact smartctl argument list and JSON field paths, within the rules
  above.
- How the Windows device names from `--scan` look and are validated.
- The file layout inside each plugin package.
