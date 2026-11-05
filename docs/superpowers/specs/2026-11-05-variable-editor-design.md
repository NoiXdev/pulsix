# Central variable editor with a source sync from the playbook — design

Date: 2026-11-05. Backlog 7.75. Owner decisions: 2026-11-03 (mockup `.superpowers/brainstorm/2836-1793712935/content/schema-sync.html`, approved, git-ignored; recorded in the backlog row) and 2026-11-05 (four questions in chat, recorded under "Owner decisions" below). Code facts behind this document: `internal/inventory/schema` (field model), `internal/templates`, `internal/worker/sync.go`, `internal/jobs/queue_sql.go`.

## Intent

A job template's form of extra variables is typed in by hand today, field by field, although the playbook it runs usually already says which variables it takes: roles declare them in `meta/argument_specs.yml`, plays ask for them with `vars_prompt`. The two drift apart as the playbook changes. The owner wants the form to come from the playbook and stay in step with it, while a person can still adjust a field, add their own, and decide which ones the form shows. The editor itself is cramped (a nested builder inside a dialog) and lives in the inventory feature although three places use it; it becomes one shared, full-width editor.

Success: pointing a template at a playbook fills its form with the right fields without typing; when the playbook changes, the template shows what changed and nothing disappears without a person confirming it; a playbook author can make any play variable a form field with one comment, following a written reference precise enough for another tool to author against.

## Owner decisions

From 2026-11-03 (backlog row 7.75):
- The source is the **playbook**. Only the roles the playbook uses count: `roles:` entries and static `import_role` / `include_role`. A dynamic role name (a Jinja expression) cannot be resolved and shows a hint.
- Variables the playbook already sets (`vars:` of the play, role parameters in `roles:` entries, `vars:` on a role import) are marked **"set in playbook"** and are not offered by default.
- An **inactive** field is hidden from the form and not sent as an extra var.
- Default activation: required-without-default fields are active, everything else inactive. An automatic sync adds new required fields active and new optional ones inactive.
- Each field shows its **origin**: from source (locked), adjusted (kept on sync), manual, gone from source.
- **Manual or automatic** mode per template. A sync shows a **preview** of added, changed, adjusted and removed fields. A **removal is always confirmed**, in either mode.
- **Inline marker** syntax: `name: value  # pulsix: [enum[a,b]] [secret] [required] Label`, the value is the default, the type is inferred from it. Unreadable markers are warnings with line numbers in the preview.
- A **sidecar** `<playbook>.argument_specs.yml` declares variables of the playbook itself.
- The **worker** reads the sources during the repo sync; the manager never clones.
- The field list takes the **full width**; one field opens in a **dialog**; sub-fields of a list are edited inline in the expanded row.
- A **formal reference** of the markers and the sidecar in the user docs (grammar, types, examples, error cases).

From 2026-11-05 (chat):
1. **Where the sync applies:** only to an organization's job templates. Inventory groups and shared (instance) template definitions get the new editor, with manual fields only.
2. **Which files:** the repo sync discovers the repo's playbooks and reads their variable sources, and records the commit it read. In the template, a playbook is picked from the repo's discovered list. (Today a sync creates no playbook rows and the web app can only create inline playbooks, although the docs say a repo lists its playbooks.)
3. **Inline playbooks** are a source too: the manager reads the stored playbook text itself (no clone involved). Roles cannot be resolved for an inline playbook; the preview says so.
4. **A field that turns inactive, or is removed after it went from the source, keeps its stored value**, but the value is neither validated nor sent. Turning the field active again (or adding it back) brings the value back.

## Goals

### 1. One variable editor, its own feature, full width

- The schema editor moves out of `web/src/features/inventory/schema` into its own feature (`web/src/features/variables`), used by inventory groups, job templates and shared template definitions.
- It is a page, not a nested builder in a dialog: a full-width field list (name, type, required, default, origin, active), "+ Field", and one field opens in a dialog. List-like fields (`repeater`, `object`) expand in their row, and their sub-fields are edited inline there.
- Job templates get a variables page (the template dialog keeps the other template settings and links to it, or the template becomes a page — the builder's call). Inventory groups keep their Schema tab, now with the new editor. Shared definitions keep their page, with the new editor and no secret type, as today.
- The values form (`SchemaForm`) moves with it and shows only active fields.

### 2. Field model additions (additive, wire-compatible)

Each field gains:
- `active` (bool; absent means active, so every stored schema keeps today's behaviour);
- `origin`: `manual` (absent means manual), `source`, `adjusted`, `gone`;
- `source`: where it came from — kind (`argument_spec`, `vars_prompt`, `marker`, `sidecar`), role and entry point if any, file and line;
- `set_in_playbook` (bool), for a variable the playbook already sets;
- for `adjusted` fields, the source's version of the field as last read, so the dialog can show "source says: 80" and offer "Reset to source".

Groups and shared definitions only ever store `manual` fields. Validation (`schema.Check`) accepts the new attributes everywhere; `origin` other than `manual` is refused on groups and shared definitions.

### 3. Sending and validation follow `active`

- A run receives the stored value of an active field only. A value stored under an inactive field, or under a name a person removed from the schema, stays in the database (sealed if secret) and is not sent. *(Controller's call:)* a field marked `gone` but not yet removed behaves as before — sent if active — until a person confirms the removal, so a source change alone never changes what a run receives.
- `schema.Validate` ignores values under inactive fields (no "undeclared key" problem, no type check), and "required" applies to active fields only. The same holds for overrides from schedules, webhooks and run-with-overrides.
- A template without a schema behaves exactly as today (every stored key is sent).
- Schema defaults are still not sent: the playbook or role applies its own default. The form shows the default as a placeholder, as today.

### 4. The repo sync reads variable sources

On every successful repo sync the worker, after fetching:
- records the **commit** it checked out;
- **discovers playbooks**: YAML files under the repo's subpath whose top level is a list of plays (mappings with `hosts`, or `import_playbook`), skipping `roles/`, `group_vars/`, `host_vars/`, `collections/`, hidden directories and the galaxy install directory;
- for each playbook, reads its **variable sources** (section 6) and reports a parsed result: the candidate fields, the "set in playbook" names, the roles it uses, and warnings and hints with file and line.

The manager stores, per repo, the commit, the time, and per playbook path its parsed result, replacing the previous sync's record. A discovered playbook can be picked in the template's playbook choice; picking it creates or reuses the `playbooks` row (`repo_id` + `path`). Inline playbooks are parsed by the manager from their stored text whenever the playbook is saved, with the same parser (a shared Go package used by worker and manager).

The parser searches roles where the run will find them, in `ansible-playbook`'s order: `roles/` next to the playbook (`<playbook dir>/roles`), then the roles installed from `requirements.yml` (the run's `roles_path`), then the playbook's own directory (corrected by backlog 7.90: the run never searched the subpath's root `roles/` for a playbook in a subdirectory). It does not read role variables from anywhere a run would not load them. (Today the sync installs galaxy roles into a directory the run's roles path does not include; that is a separate defect, see "Found while specifying".)

### 5. Template sync: modes, preview, apply

- A template whose playbook has a parsed source can link its variables to it. Mode is **off** (no link; all fields manual, today's behaviour), **manual**, or **automatic**. Linking a template for the first time shows the preview with every candidate as "added", with the default activation applied.
- **Manual mode:** "Sync now" on the variables page compares the template's fields with the newest parsed source and shows the preview; nothing changes until the person applies it. The page shows the source's commit and sync time, and whether a newer commit than the one last applied exists.
- **Automatic mode:** after each successful repo sync the manager applies additions and changes to every linked template of that repo's playbooks. Removals are never applied automatically: a field gone from the source is marked `gone` and the template shows "N fields gone from the source — review"; removing them is a confirmed action.
- *(Controller's call)* **Changes held for confirmation in automatic mode, like removals:** a change that would make a stored value invalid (type change, an enum losing the stored option, a tighter min/max or pattern), and any change of the secret flag (adding or dropping `no_log`). The manager never seals or unseals a stored value on its own; a person applies such a change in the preview, and a value that no longer fits must be re-entered there. The template shows "N changes need review" beside the gone count. New required fields without a default are still added active in automatic mode (owner decision); the template then shows "N required fields have no value", and schedules and webhooks that override extra vars fail validation until a value is set — the same failure the role's own argument check would cause at run time.
- *(Controller's call)* **"Gone" means absent from a source that was read.** A field is marked `gone` only when its playbook and role were read without error and no longer declare it. A playbook that is now invalid YAML, a role that is no longer found, a playbook left out by a size cap, or a role that became dynamic keeps the template's previous state and shows a warning; nothing turns `gone` because of it.
- **Preview** lists: added (with the activation they will get), changed (field by field, old → new), adjusted (the source changed a field the template has adjusted; the adjustment is kept, the new source version is shown), gone (with a checkbox "also remove gone fields", off by default), unchanged count, and the parser's warnings and hints.
- **Origins on apply:** a new field from the source is `source` (locked: editing a locked attribute in the field dialog makes it `adjusted`); "Reset to source" makes it `source` again; `manual` fields are never touched by a sync; the active switch is the person's choice and is never changed by a sync after the field was added; a field that comes back to the source while `gone` returns to `source` (or `adjusted` if it was adjusted).
- **Concurrency:** job templates gain a `revision` and the template write honours `If-Match` the way inventory group writes do (only checked when sent; the SPA always sends it). An automatic sync bumps the revision, so a person's stale save answers 412 and the page offers a reload.
- The field dialog shows where the field is overridden: schedules and webhooks of this template whose overrides name it ("Used in").

### 6. Source formats and mapping

*(Controller's call)* **`import_playbook` is followed:** the variable sources of a playbook include those of the playbooks it imports (paths relative to the importing file, inside the repo subpath only), up to a depth of 3, with cycles cut and a warning. Sources of imported plays rank after the importing file's own, in import order; "set in playbook" applies across all of them. A dynamic import path is a hint, as for dynamic roles.

*(Controller's call)* **Origin and activation are tracked on top-level fields only.** A field from the source that has sub-fields (`object`, `repeater`) is one unit: its sub-fields follow the source and cannot be switched inactive. Editing a sub-field makes the whole top-level field `adjusted`; once adjusted, a person may also add manual sub-fields to it, and a later source change to its sub-fields is shown in the preview under "adjusted" with "Reset to source", not merged.

Precedence when one variable name comes from several sources (the first wins, the others are noted as hints): sidecar, then inline marker, then `vars_prompt`, then roles in the order the playbook uses them.

- **Role argument specs** (`<role>/meta/argument_specs.yml`, entry point `main`, or the `tasks_from` entry point of an `import_role` / `include_role` when given): `type` `str`/`path` → string, `int` → int, `float` → float, `bool` → bool, `list` with scalar `elements` → list, `list` with `elements: dict` and `options` → repeater, `dict` with `options` → object; `choices` → enum (string choices; non-string choices are converted to strings with a warning); `no_log: true` → secret (its default dropped with a warning, since secrets carry no default); `required`, `default`, `description` (first line → label when no label is given, the rest → description). Types with no counterpart (`raw`, `dict` without options, `bits`, `bytes`, `jsonarg`, `json`, `sid`) are skipped with a warning.
- **`vars_prompt`:** `name`, `prompt` → label, `private: true` → secret, `default` → default; the type is string (a prompt has no type); a prompt without a default is required.
- **Inline marker** on a play `vars:` entry: `name: value  # pulsix: [enum[a,b]] [secret] [required] Label`. The value is the default and its YAML type sets the field type (string, int, float, bool, list of scalars → list). `enum[...]` makes an enum whose options are the listed strings; the value must be one of them. `secret` makes a secret (the value is not used as default). `required` makes it required. The rest of the comment is the label. A play variable with a marker is a source field, not "set in playbook".
- **Sidecar** `<playbook-file-name>.argument_specs.yml` next to the playbook: the same format as a role's `meta/argument_specs.yml` with the entry point `main`.
- **Names** outside the field name rule (lowercase `[a-z_][a-z0-9_]{0,63}`, not `ansible_*` / `pulsix_*`) are skipped with a warning. Nesting beyond 4 levels or more than 100 fields per level is cut with a warning.
- **Warnings** carry file and line and never stop the sync. A file that is not valid YAML is one warning for that file.

### 7. User documentation

In noix-docs (branch `pulsix`):
- a new reference page for the inline markers and the sidecar: grammar (EBNF), type inference, every keyword, the mapping table from argument specs, precedence, limits, examples, and every warning with its cause;
- updates to "Inventory and variables", "Playbooks and jobs" (repos discover their playbooks; the template variables page; modes and preview), and "Shared resources" (manual fields only).

## Non-goals

- A sync for inventory group schemas or for shared template definitions (owner decision 1).
- Sending schema defaults as extra vars.
- Reading variables from `group_vars` / `host_vars`, `defaults/main.yml` without argument specs, or included task files.
- Pinning a run to the commit a sync read; runs keep using the branch tip.
- Fixing the galaxy roles path of runs (filed separately).

## Anti-goals

- A field, or a stored value, disappears without a person confirming it.
- A sync overwrites something a person adjusted.
- An unreadable source stops a sync or blocks a run.
- A template's runs change behaviour because the template was linked, without the person having seen the preview.

## Constraints

- Layering per `CLAUDE.md`; the parser is a pure Go package with table-driven tests over fixture repos; no YAML library beyond `gopkg.in/yaml.v3` (already a dependency).
- Migrations additive. Wire changes additive: a worker one minor version older does not report sources, and the manager then keeps the previous record and shows "the worker that synced this repo does not read variable sources".
- Worker→manager limits: an event payload is capped at 1 MiB and a message at 16 MiB. The builder chooses how parsed sources travel (several events, the result stats, or a new RPC), but a large repo must not lose the whole record: past a cap, the record is marked incomplete with a warning naming the playbooks left out.
- Bounded parsing: at most 1 000 playbook files per repo, files over 512 KiB skipped with a warning (the existing playbook size cap).
- English in code; en and de i18n for every new string; end-user docs updated in the same work item.
- Permissions: linking, mode, preview and apply need `templates.write`; "Sync now" on the page re-reads the stored source and needs `templates.write`; fetching the repo anew needs `repos.write` as today (the page links to the repo's sync).

## Left to the builder

- Storage shape of the per-repo parsed sources (table vs. jsonb on `git_repos`).
- Route names of the template variables page; whether the template itself becomes a page.
- How the preview is computed (manager-side pure function over stored fields and parsed source is preferred, so it is unit-testable).
- The exact wording of warnings, as long as the reference page lists each one.

## Found while specifying

- The sync installs galaxy roles into `<repo>.galaxy`, but the run's `ansible.cfg` sets no `roles_path` including it (`internal/worker/runner/layout.go`), so roles from `requirements.yml` may not be found at run time. File as its own backlog item.
- The user docs say a repo lists its playbooks; that becomes true with goal 4.
