# SNMP checks (backlog 3.13d) — design

Status: the owner accepted the proposal on 2026-11-14 ("passt weiter"):
`snmp` is a manager probe against the host's address; credentials live in a
new per-organization secret store on the manager (named secrets with
fields, encrypted at rest like other sealed data, write-only, names only in
the API and GUI), referenced by the existing `secret` parameter; v2c and v3
(no v1); params `secret`, `version`, `port`, `timeout_seconds`, `oids`
(`name=OID`, at most 32) and `interfaces` (the device's IF-MIB table);
metrics `snmp.up`, `snmp.response_ms`, `snmp.uptime_seconds`, `snmp.value`,
`snmp.text`, `snmp.if.status` (`up`/`down`/`admin_down`), `snmp.if.in_bps`,
`snmp.if.out_bps`, `snmp.if.errors`; two seed rules ("SNMP device
unreachable", "Interface down") with a backfill migration; a settings page
for secrets; a secret still used by a check cannot be deleted. Not part of
it: traps, arbitrary walks, MIB uploads, SNMP through the agent. As the
backlog row decided, `internal/probe` is split into one package per probe.
Every other decision below is **(Claude's call)**.

## Why

Switches, printers, UPSs and firewalls cannot run an agent; SNMP is how
they are watched. The manager already probes hosts (`tcp`, `http`, `tls`,
`dns`, `icmp`), so `snmp` joins them. Zone workers as vantage points come
with 3.13a.

## Probe package split (first, no behaviour change)

`internal/probe` becomes a small core (`Observation`, `Guard`, param
helpers, `Run` dispatching through a registry map of plugin name → probe
func) plus `internal/probe/{icmp,tcp,http,tls,dns,snmp}`, each exporting
one `Probe` func. Tests move with their code. `probe.Observation` gains
`Item string` and `Label *string` (a probe result can now be per item and
discrete); the runner passes both into `checks.Observation`. Existing
probes keep `Item == ""` and no label.

## Secret store on the manager

- **Table** (migration 00096) `org_secrets`: `org_id`, `name` (the
  agent.yaml secret-name pattern `[A-Za-z0-9_.-]{1,64}`, unique per org),
  `fields_sealed bytea` (the JSON object of fields, sealed with the
  manager's box and the AAD `org_secrets|<org_id>|<name>` so a row cannot
  be moved to another org or name), `field_names text[]` (sorted, clear —
  shown in the GUI), `created_at`, `updated_at`. Additive; Down drops it.
- **Fields:** 1–16 per secret, field names the same pattern, values
  1–4096 bytes, valid UTF-8, no NUL. No other structure.
- **Package** `internal/orgsecrets` (service + store, the layering of
  CLAUDE.md): `Put(org, name, fields)` (create or replace whole),
  `List(org)` (name, field names, timestamps, the check keys using it),
  `Delete(org, name)` (`ErrConflict` naming the check keys when any check
  definition — org, group, host or shared projection — names it in its
  `secret` param), and `Fields(org, name)` for the probe runner only. No
  read path returns a value.
- **API:** `GET /orgs/{org}/secrets`, `PUT /orgs/{org}/secrets/{name}`
  (body `{fields: {…}}`), `DELETE /orgs/{org}/secrets/{name}`; writes need
  `credentials.write` (the permission for SSH keys, the other stored
  credentials), the list needs `checks.read` (it carries names only). Every write is audited (name and
  field names, never values). Error texts never carry a value.
  `PUT` with `If-None-Match: *` creates only (412 when the name exists;
  the SPA sends it on create), any other `If-None-Match` is a 400; without
  it `PUT` creates or replaces. The delete 409 lists the check keys as
  error details (location `used_by`, the key as the message) next to its
  text.
- **Not in backups' clear text:** the sealed column is backed up as it is
  (the backup already carries the master key's ciphertexts the same way).

## The `snmp` plugin

Descriptor (`internal/checkplugins/snmp.go`, `Runs: Server`):

| Param | Type | Rule |
|---|---|---|
| `secret` | `SecretParam()` | required |
| `version` | enum `v2c`, `v3` | default `v2c` |
| `port` | integer 1–65535 | default 161 |
| `timeout_seconds` | integer 1–30 | default 5 |
| `oids` | list, at most 32 | each `FormatSNMPOID`: `name=OID`, name the secret-name pattern, OID numeric dotted (`1.3.6…`, optional leading dot, at most 128 arcs, each arc ≤ 2³²−1, and BER-encodable per X.690 8.19: first arc 0–2, second ≤ 39 under 0 and 1, 40·first+second ≤ 2³²−1); names unique |
| `interfaces` | bool | default false |

`Check` hook: at least one of `oids` non-empty or `interfaces` true.

Metrics:

| Metric | Kind | Item | Value |
|---|---|---|---|
| `snmp.up` | numeric | — | 1 when the device answered, else 0 |
| `snmp.response_ms` | numeric | — | the first request's round trip |
| `snmp.uptime_seconds` | numeric | — | sysUpTime (`1.3.6.1.2.1.1.3.0`) ÷ 100 |
| `snmp.value` | numeric | oid name | a numeric OID's value (Integer, Counter32/64, Gauge32, TimeTicks, Uinteger32); counters as raw values |
| `snmp.text` | discrete | oid name | an OctetString/ObjectIdentifier/IPAddress value as text, cleaned like `script.output` (512-byte label bound) |
| `snmp.if.status` | discrete | interface | `up` (oper up), `down` (admin up, oper not up), `admin_down` (admin down) |
| `snmp.if.in_bps` / `snmp.if.out_bps` | numeric | interface | bits per second from `ifHCInOctets`/`ifHCOutOctets` (fallback 32-bit `ifInOctets`/`ifOutOctets`) between two polls |
| `snmp.if.errors` | numeric | interface | (`ifInErrors` + `ifOutErrors`) per second between two polls |

- An OID the device does not have (NoSuchObject/NoSuchInstance/
  EndOfMibView) is an error observation on its item ("the device has no
  such object"), not `up = 0`.
- Interface item: `ifName` (IF-MIB ifXTable), else `ifDescr`, else
  `if<ifIndex>`; an item that is not `checks.ReportableItem` falls back to
  `if<ifIndex>`; duplicate names get ` (<ifIndex>)` appended. At most 128
  interfaces per poll, in ifIndex order; the rest are skipped, and the
  runner logs that at most once per check per hour.
- Items: `ItemParam` is `oids` (item = the entry's name); with
  `interfaces` true the descriptor's item hook accepts any item (interface
  names are the device's).

### Running it (`internal/probe/snmp`)

- Dependency: `github.com/gosnmp/gosnmp`, pinned to a release at least two
  weeks old.
- The host's address is resolved and checked by the probe `Guard` like
  every probe; the checked IP literal is what gosnmp dials (UDP). A refused
  target is a collection error on every metric, never `up = 0`.
- v2c: community from the secret's field `community`. v3: `username`,
  `auth_protocol` (`SHA`, `SHA-256`, `SHA-512`), `auth_password`, optional
  `priv_protocol` (`AES`, `AES-256`) and `priv_password` (authPriv when
  priv is set, authNoPriv otherwise; noAuthNoPriv is refused). A missing
  or invalid field → every metric an error naming the secret and the
  field, never a value.
- One `Get` for sysUpTime plus the OIDs (batched at 16 per PDU); with
  `interfaces`, BulkWalks of the needed IF-MIB columns (v2c and v3 both
  support GetBulk). The whole poll is bounded by `timeout_seconds` per
  request and the probe's overall deadline; retries 1.
- Rates: the runner keeps, per slot, the previous poll's counters and
  sysUpTime in memory. No previous poll, a counter that went backwards, or
  sysUpTime that went backwards (reboot) → no rate this round (no
  observation for that metric), never a negative or huge value. A manager
  restart or lock handover starts over. A 32-bit counter wraps once at 2³²
  (handled), a second wrap within one interval is not detectable
  (documented).
- Output never quotes a secret; device strings are cleaned (UTF-8, no
  control characters) before they become items or labels.

### Runner (`internal/probes`)

`Deps` gains `Secrets` (`Fields(ctx, org, name) (map[string]string,
error)`, nil → every secret-using probe fails with "no secret store").
Before a probe whose descriptor `UsesSecret()`, the runner looks the
secret up; a missing secret → every metric an error `secret "<name>" is
not defined`, the probe is not run. Secrets are passed to the probe as a
separate argument, never merged into params (params are logged on errors
elsewhere). The runner owns the per-slot rate state and drops a slot's
state when the slot goes.

### Host page warning

`checks.MissingSecret` today compares a check's secret with the names the
host's agent reported. For a `Runs: Server` plugin it compares with the
org's secret store instead (names only): the host page warns
`secret "<name>" is not defined` before the first poll fails.

## Default rules

Two seed rules, scope org, enabled: "SNMP device unreachable"
(`snmp.up` below 1, severity crit) and "Interface down" (`snmp.if.status`
in `down`, severity warn). `SeedRules` gains them; migration 00097
backfills them for existing organizations without a rule of that name,
exactly like 00094.

## SPA

- Settings → "Secrets" / "Secrets": the list (name, field names, last
  change, used by), a dialog to create or replace a secret (rows of field
  name + password input; replacing means entering every field again; the
  dialog says values are never shown again), delete with the conflict
  message naming the checks.
- The checks dialog offers `snmp` from the descriptor (schema, fixture,
  en/de plugin name and parameter texts per the "Adding a plugin" note);
  the `secret` help text for a server plugin points to Settings → Secrets
  instead of agent.yaml.
- en/de metric names for the new metrics where the SPA has them.

## Tests

- Package split: the existing probe tests pass unchanged in their new
  packages; `probe.Run` dispatches every plugin; the runner passes Item
  and Label.
- `orgsecrets`: validation (names, field count/size, NUL, UTF-8), sealing
  with AAD (a row copied to another org or name fails to open), replace,
  list never carries values, delete conflict naming checks (each binding
  kind and shared), integration against the Postgres container; API tests
  (permissions, 404, 409, audit entries without values).
- `snmp`: descriptor golden tests and the format; the probe against a
  fake SNMP agent (an in-test UDP responder built with gosnmp's packet
  types, or a fake client interface for logic) covering v2c, v3
  authNoPriv/authPriv field handling, missing fields, OIDs of each type,
  no-such-object, interface naming and fallbacks, the 128 cap, statuses;
  rates (first poll none, normal, 32-bit wrap, counter reset, reboot);
  guard refusal; timeout. One test against a real SNMP daemon in a
  throwaway container (`docker run --rm --name pulsix-sdd-snmpd-…`,
  skipped when Docker is unavailable) for v2c and v3.
- Runner: secret lookup, missing secret, secrets not in params, rate
  state lifecycle.
- Seed rules, catalogue, migration rows (integration).
- SPA: secrets page (list, create, replace, delete conflict), `snmp` in
  the dialog, the server-plugin secret help.

## Documentation

`docs/BACKLOG.md` (3.13d done; 3.6h's SNMP half pointed here),
`docs/upgrades.md` (migrations 00096/00097, the new dependency),
noix-docs: a new page on SNMP checks (versions, secret fields, params,
metrics, rates and their limits, default rules, examples), a page or
section on secrets in the organization settings, check-plugins reference,
changelog. No tech stack named.

## Non-goals

Traps; arbitrary subtree walks; MIB files and symbolic OID names; SNMP v1;
SNMP from agents or zone workers (3.13a); writing (Set); per-interface
include/exclude filters (a later row if wanted).
