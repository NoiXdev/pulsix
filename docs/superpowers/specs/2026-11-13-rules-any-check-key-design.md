# Alert rules for checks under any key (backlog 3.13e) — design

Status: the owner accepted the proposal on 2026-11-13 ("passt"): org,
group and host rules apply to every check that reports the rule's metric,
whatever its key; an item-scoped rule names the check's key as well,
defaulting to the plugin's; the check form's hint says rules apply to every
key. Every other decision below is **(Claude's call)**.

## Intent

A rule names a metric (`nginx.up`), and the service derives the rule's
check key from the metric's plugin (`nginx`). Resolve matches a host's
tuples on that key, so a second `nginx` check on a host (key
`nginx-internal`), a copied disk check or a second `http` check gets no
rule at all — not even the seeded ones. The check form prefills the plugin
name as the key and warns about exactly this (`checks.rulesKey`). With the
web server and database checks (3.10, 3.11) several checks of one plugin
per host are normal.

## Matching

- A metric name belongs to exactly one plugin (the catalogue is built from
  the descriptors, metric names carry the plugin's prefix; this change adds a test that
  no two descriptors share a metric name). So "the tuple's metric is the
  rule's metric" already implies "the tuple comes from a check of the
  rule's plugin". Org-, group- and host-scoped rules match every tuple of
  the host whose **metric** equals the rule's metric, under any check key
  (`rules.Resolve`, `addToEveryMatchingTuple`).
- An item-scoped rule names one tuple: host, check key, item, metric. Its
  check key is new caller input, `item_check_key` (optional; empty means
  the plugin's key, today's behaviour). It must be a valid check key (the
  same format check definitions accept); it need not exist on the host yet,
  as the item need not.
- The rule's `check_key` column keeps the plugin's key (derived, as today):
  it is what the rules page filters on and what `checkTreeBelongsTo`
  checks an expression's metrics against. The instance key of an
  item-scoped rule is stored in a new nullable column
  `check_rules.item_check_key` (additive migration; NULL for every
  existing rule and for every other scope; a CHECK that it is NULL unless
  scope is `item`).
- Evaluation is per tuple and already keyed by the tuple's own check key
  (windows, states, alerts, item decisions), so nothing else changes: two
  `nginx` checks on a host give two independent states and alerts.
- Precedence between rules is unchanged (scope depth, then priority); a
  host-scoped rule now competes for the tuples of every check of that
  plugin on the host, which is the intent.

## API and SPA

- Rule DTOs (create, update, read, effective rules) gain `item_check_key`
  (string, optional; omitted when empty). The OpenAPI document and
  `schema.d.ts` regenerate.
- Rule dialog, item scope: a "Check" select listing the chosen host's
  checks that run the metric's plugin (from the host checks the SPA
  already reads: key + plugin), preselected to the plugin's key when it
  exists, else the first; the item picker then offers that check's items
  (it filters on `check_key` already). The rules list shows the key next
  to the item when it is not the plugin's.
- Check form: the hint `checks.rulesKey` (en/de) changes from the warning
  to a statement that alert rules on `<plugin>.*` metrics apply to every
  check of this type, whatever its key.

## Behaviour change

After an upgrade, every existing org/group/host rule — the seeded ones
included — starts judging checks under non-default keys too: a copied disk
check or a second `http` check can raise alerts it never raised before.
That is the point of the change; `docs/upgrades.md` says so.

## Tests

- `rules.Resolve`: an org/group/host rule matches tuples under the default
  key and under two other keys of the same plugin; it does not match a
  tuple of another plugin; an item rule with `item_check_key` matches only
  that key; an item rule without it keeps today's default.
- Service: `item_check_key` refused on a non-item scope, refused when not
  a valid check key, stored and read back; the expression guard unchanged.
- Store/migration: the column round-trips; the CHECK refuses it on other
  scopes.
- Evaluation end to end (`internal/rules` with the database): two checks
  of one plugin on one host under different keys, one rule, both tuples
  judged independently.
- API through `apitest`: create/read an item rule with `item_check_key`;
  the effective rules of a host list the tuples of a non-default key.
- SPA (Vitest): the check select appears for item scope, filters by
  plugin, defaults to the plugin's key, the item picker follows it, the
  payload carries `item_check_key` only when it differs from the plugin's
  key (or always for item scope — builder's call, say which); the new
  check-form hint.

## Documentation

- `docs/BACKLOG.md` 3.13e done; `docs/upgrades.md` behaviour change and
  migration; noix-docs: the rules page (rules apply to every check of the
  plugin; item rules name the check), the checks page hint, changelog.

## Non-goals

- Rules that target one check key at org/group/host scope (e.g. "only
  `nginx-internal` on every host"). Not asked for; a later row if wanted.
- Renaming or re-keying existing checks.
