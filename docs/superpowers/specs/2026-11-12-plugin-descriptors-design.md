# One descriptor per check plugin (backlog 3.54) — design

Status: the owner agreed on 2026-11-12 to give every check plugin one
descriptor on the manager and to build the SPA's check form from it, before
3.10 and 3.11 add plugins ("ja mach so", "weiter"). Every decision below is
**(Claude's call)** unless it restates that.

## Intent

3.52 made an agent plugin one folder. On the manager and in the SPA a plugin
is still spread over about ten places: the parameter validation
(`internal/checks/*_params.go`, `definitions.go`), the metric catalogue
(`internal/rules/catalog.go`), the plugin enums of three API handlers and
their doc strings, `itemParam` (`internal/checks/items.go`), the plugin lists
of `CheckDialog.tsx` and `SharedCheckFormPage.tsx`, `NUMERIC_PLUGIN` and
`DISCRETE_METRICS` (`web/src/features/hosts/hostChecks.ts`), the SPA field
tables (`syntheticParams.ts`, `hardwareParams.ts`) and i18n. 3.12 and 3.9 each
missed one in review. After 3.54 a manager-side plugin is one descriptor file
plus its UI texts.

## Goals

- **No behaviour change.** Every plugin accepts and refuses exactly the
  parameters it does today; every metric keeps its name, kind and check key;
  the API keeps its routes, shapes and enums; the agent is untouched.
- One leaf package, `internal/checkplugins`, holds one descriptor per plugin
  (one file each) and the registry of all of them. It imports nothing from
  the rest of Pulsix, so `internal/checks`, `internal/rules` and
  `internal/api` can all derive from it.
- The SPA reads the descriptors from the API and renders the parameter
  fields and their client-side validation from them.

## The descriptor

Per plugin:

- `Name` (`"disk"`), `Runs` (`Agent` or `Server` — the synthetic plugins run
  on the server).
- `Params`: an ordered list of parameter specs. A spec has a name and a type:
  `string`, `integer`, `bool`, `enum` (with options), or `list` (of strings);
  plus, as the type needs, `required`, a default (documentation only — the
  manager keeps storing exactly what the client sent), `min`/`max`,
  `maxEntries`, `maxBytes`, `nonEmpty`, and an optional named **format**
  (below).
- `ItemParam`: the list parameter that enumerates the plugin's items, if any
  (today: disk `mounts`, network `interfaces`, service `units`, temperature
  `sensors`, smart `devices`, raid `arrays`), and whether item `""` stays asked
  when that list is set (the 3.9 hardware plugins) — the same rules
  `AsksForItem` applies today, including smart's `<path> [<type>]` match.
- `Metrics`: name, kind (`numeric` or `discrete`).
- An optional `Check` hook for rules that span parameters or need code (e.g.
  `http.url` must parse once `{address}` is substituted) — kept as small as
  today's code allows.

**Formats** are named validators implemented once in Go
(`url`, `dnsName`, `server`, `dockerLabel`, `dockerSocket`, `devicePath`, … —
exactly the special rules the `*_params.go` files carry today). The SPA has a
TypeScript twin per format. Parity is pinned by a shared test-vector file
(`internal/checkplugins/testdata/formats.json`: format, input, valid) that a
Go test and a Vitest test both read.

## Manager

- `internal/checks`: `supportedPlugins`, `IsSynthetic`, `validateParams`,
  `itemParam`/`AsksForItem` and the plugin list in error messages derive from
  `checkplugins`. The `*_params.go` files go; their tests move to
  `checkplugins` or stay as behaviour tests through
  `ValidateDefinitionShape` — every existing accept/refuse case keeps
  passing.
- `internal/rules`: `Catalogue()` is built from the descriptors' metrics
  (check key = plugin name, as today).
- `internal/api`: the plugin fields' `enum` comes from the registry (a
  `huma.SchemaProvider` type, or the generator's equivalent) instead of a
  struct-tag literal; the `params` doc strings are generated from the specs.
  New route **`GET /api/v1/check-plugins`** (any signed-in user; static data)
  returns the descriptors without hooks: name, runs, params with their
  constraints and format names, item param, metrics with kind.
- Error messages for refused parameters keep their meaning; exact wording may
  become uniform where no test or document pins it.

## SPA

- A hook loads `GET /check-plugins` once per session (it never changes while
  the manager runs).
- `CheckDialog` and `SharedCheckFormPage` take the plugin list and each
  plugin's fields from it; one generic field renderer covers the five types
  (number input, text input, select, switch, multi-line list), and client
  validation applies the same constraints and the TypeScript formats.
- Labels, hints and option labels stay in i18n, keyed by plugin and
  parameter; a test fails when a descriptor parameter has no en or de label.
- `NUMERIC_PLUGIN` and `DISCRETE_METRICS` derive from the metric kinds.
- Behaviour the SPA has today stays: untouched fields are not sent (the
  server applies defaults), tcp `send`'s `\r\n\t\\` escapes, the
  "runs from the Pulsix server" marker for `Runs = Server`, list fields where
  empty means `{}`.

## Agent

Untouched. A test that may import both sides (in `internal/checkplugins`'
tests) checks that every `Runs = Agent` descriptor has a plugin in the
agent's `plugins.Registry()` and vice versa.

## Non-goals

- Generating agent code or agent-side parameter parsing from descriptors.
- Changing any parameter, metric, default or API shape.
- Rules for checks under a non-default key (backlog 3.13e).

## Documentation

- `docs/BACKLOG.md`: 3.54 done.
- The "Adding a plugin" note (agent `plugins/registry.go`) and a package doc
  in `internal/checkplugins` describe the new path: agent folder + one
  descriptor + i18n texts + user docs.
- No user-visible change, so no end-user docs.

## Left to the builder

- The exact Go types and the huma mechanism for the computed enum.
- How the descriptor list is ordered (the SPA shows today's order).
- Which formats exist (exactly those today's code needs).
