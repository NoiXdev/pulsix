# Central Variable Editor with a Playbook Source Sync (7.75) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A job template's variable form comes from its playbook (role argument specs, `vars_prompt`, inline markers, a sidecar) and stays in step with it through a previewed, never-destructive sync, edited in one shared full-width editor that inventory groups and shared definitions use too (manual fields only).

**Architecture:** A pure parser package (`internal/varsource`) runs on the worker during every repo sync and in the manager for inline playbooks; the worker returns its report inside the sync result's stats, the manager strips it from `jobs.stats` and stores it per repo. A second pure package (`internal/varsync`) computes the preview and applies it to a template's fields; `internal/templates` owns linking, mode, apply and the automatic apply after a sync. The field model gains `active`, `origin`, `source`, `set_in_playbook` and `source_field`; validation and the claim follow `active`. The SPA moves the schema editor into `features/variables` and gives templates a variables page.

**Tech Stack:** Go 1.27 (pgx v5, sqlc, goose, huma, `gopkg.in/yaml.v3`), PostgreSQL 18 + TimescaleDB, React/TypeScript SPA (Vitest, Playwright).

**Spec:** `docs/superpowers/specs/2026-11-05-variable-editor-design.md` (owner decisions and the "Controller's call" items are binding).

## Global Constraints

- Everything in the repository is English; every new user-visible SPA string in English and German (`web/src/i18n/en|de/*.ts`). Public texts never name the tech stack.
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal change.
- Tests per package with a timeout (`go test -race -count=1 -timeout 900s ./internal/<pkg>/`; `internal/api` needs `-timeout 1800s`; `internal/e2e` on its own invocation). Never one whole-module `go test ./...`. Full suite: `make test`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`; in `web/`: `npx vitest run <file>`, `npx eslint <files>`, `npx tsc -b`; OpenAPI types via `npm run api:gen`. No prettier.
- sqlc is not installed as a binary: `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`, then `... diff` must be clean.
- One additive migration for the feature: `internal/db/migrations/00081_variable_sources.sql` (Task 5), with a working Down. Never edit a released migration.
- Database tests use `internal/db/testdb`. Only touch Docker containers the tests start themselves.
- Layering per `CLAUDE.md`: handlers decode → service → store; services depend on interfaces declared next to them; `internal/varsource` and `internal/varsync` are pure (no I/O beyond the `fs.FS` they are handed, no DB, no HTTP). Files over ~400 lines are split (`internal/templates/service.go` is at 489, `internal/repos/service.go` at 574, `internal/api/repo_handlers.go` at 400: new code goes into new files).
- YAML: `gopkg.in/yaml.v3` only. No new Go or npm dependency.
- Limits (spec, exact): at most 1 000 playbooks per repo; files over 512 KiB skipped with a warning; `import_playbook` depth 3; nesting 4 levels; 100 fields per level; 100 enum options; a worker's encoded report at most 8 MiB (inside the 16 MiB worker→manager message; the 1 MiB event cap is not used for it).
- Permissions: GET of a template's variables `templates.read`; linking, mode, preview ("Sync now") and apply `templates.write`; a repo's discovered playbooks `repos.read`; picking a discovered playbook `playbooks.write`; fetching a repo anew stays `POST …/repos/{id}/sync` (`repos.write`).
- Wire changes additive. A worker one minor older sends no sources: the manager keeps the previous record and flags it (`worker_reads = false`).
- The noix-docs pages and the backlog rows are written by the controller (see "Controller follow-ups"), not by any task.

## Decisions this plan takes (spec left them to the builder, or they follow from the code)

1. **Transport:** the worker puts the encoded report under the stats key `var_sources` of the sync result (`ReportResultRequest.stats`, no proto change); `SQLQueue.finish` removes the key before `jobs.stats` is written and hands it to a `SourceSink`. A missing key = an older worker.
2. **Storage:** tables `repo_var_sources` (one row per repo) and `repo_playbook_sources` (one row per discovered path, `result jsonb`); `playbooks.var_source jsonb` for inline text; `job_templates.revision`, `vars_mode`, `vars_source_rev`, `vars_applied_at`.
3. **Routes:** the template stays a dialog for its other settings; its variables get the page `/o/$org/templates/$id/variables`. API under `/api/v1/orgs/{org}/job-templates/{id}/variables[...]` and `/api/v1/orgs/{org}/repos/{id}/playbooks`.
4. **Sidecar name:** the playbook file name without its extension: `deploy.yml` → `deploy.argument_specs.yml` (or `.yaml`).
5. **`vars_prompt` `private`:** absent counts as `true` (Ansible's default), so an entry without `private: false` is a secret.
6. **Automatic mode for an inline playbook** applies when the playbook is saved (the inline analogue of "after each successful repo sync").
7. **Inventory groups:** an inactive group field is also left out when the inventory is rendered (`schema.Resolve`), the group's meaning of "not sent".
8. **"Used in"** is computed in the SPA from the template's schedules and webhook triggers, which the SPA already loads (no new endpoint).
9. **Automatic apply runs after Finish commits**, synchronously, on a detached context bounded to 30 s; a failure is logged per template and never fails the sync. A manager crash in between leaves the apply to the next sync (the apply is idempotent).
10. **Values of inactive or removed fields:** on a template save the server carries stored values over for every stored key that the incoming body omits and whose field is inactive or no longer declared (verbatim, so a sealed secret stays sealed). The SPA sends active fields only.

## Review Focus

1. A playbook that becomes invalid YAML, a role that vanishes from the roles path or turns into a Jinja name, an import that becomes dynamic, a playbook left out by a cap, or a playbook deleted from the repo: no field turns `gone`, the previous state stays and the preview says why (Task 7 `TestComputeGoneOnlyFromReadSources`).
2. A secret field switched inactive, saved, then switched active again: the sealed bytes are identical before and after, and no run receives the value while it is inactive (Task 8 `TestInactiveFieldKeepsItsStoredValue`, `TestClaimSendsActiveFieldsOnly`).
3. An older worker syncs a repo that already has a record: commit and playbook rows stay, `worker_reads` turns false, the page says so, automatic templates are left alone (Task 5 `TestMarkVarSourcesUnreadKeepsTheRecord`, Task 9 `TestAutomaticApplySkipsManualTemplatesAndOldWorkers`, Task 12 page test).
4. A person saves the variables page after an automatic sync bumped the revision: 412, nothing of either write is lost, the page offers a reload (Task 8 `TestTemplateIfMatch`, Task 9 `TestAutomaticApplyLosesNoConcurrentSave`, Task 12).
5. A symlink in the clone that points outside it (e.g. a role's `meta/argument_specs.yml` → `/etc/...`) is never read (Task 4 `TestSyncExecutorDoesNotFollowSymlinksOutOfTheClone`).

---

### Task 1: Field model: `active`, `origin`, `source` (schema package)

**Files:** Create `internal/inventory/schema/origin.go`, `internal/inventory/schema/origin_test.go`. Modify `internal/inventory/schema/schema.go` (`Field` struct; `checkField` calls the new checks, which live in `origin.go`), `validate.go` (`validateObject`), `resolve.go` (`Resolve`), `internal/inventory/service.go` (`SetSchema`, ~line 353), `internal/shared/service.go` (`validateTemplateDefinitionInput`, ~line 448), their tests, `web/src/api/schema.d.ts` (regenerated: `schema.Field` is the API DTO of groups, templates and shared definitions).

**Interfaces (produced):**
```go
type Origin string     // OriginManual "manual", OriginSource "source", OriginAdjusted "adjusted", OriginGone "gone"
type SourceKind string // SourceArgumentSpec "argument_spec", SourceVarsPrompt "vars_prompt", SourceMarker "marker", SourceSidecar "sidecar"
type FieldSource struct {
	Kind       SourceKind `json:"kind"`
	Role       string     `json:"role,omitempty"`
	EntryPoint string     `json:"entry_point,omitempty"`
	File       string     `json:"file"`
	Line       int        `json:"line,omitempty"`
}
// Field gains (JSON names are the wire contract):
//   Active *bool `json:"active,omitempty"`; Origin Origin `json:"origin,omitempty"`;
//   Source *FieldSource `json:"source,omitempty"`; SetInPlaybook bool `json:"set_in_playbook,omitempty"`;
//   SourceField *Field `json:"source_field,omitempty"`
func (f Field) IsActive() bool                 // nil Active = true
func (f Field) EffectiveOrigin() Origin        // "" = manual
func (f Field) SameContent(g Field) bool       // canonical JSON of both with Active, Origin, Source, SetInPlaybook, SourceField cleared
func (s Schema) CheckManualOnly() error        // *ValidationError
func Sendable(fields []Field, values map[string]any) map[string]any // keys of active top-level fields only
```

- [ ] **Step 1: Failing tests** in `origin_test.go`:
  - `TestCheckOriginAttributes` (table, problem path + message substring): origin `"elsewhere"` → `fields[0].origin` "unknown origin"; origin `source` without `source` → `fields[0].source`; `manual` with a `source` → `fields[0].source`; `source.kind` `"x"` → `fields[0].source.kind`; `source.file` `""` → `fields[0].source.file`; a sub-field carrying any of `active`, `origin`, `source`, `set_in_playbook`, `source_field` → `fields[0].fields[0].<attr>` "only top-level fields"; `source_field` on origin `source` or `manual` → `fields[0].source_field`; `source_field` whose name differs → `fields[0].source_field.name`; `source_field` failing its own check (enum without options) → a problem under `fields[0].source_field`; an `adjusted` field with a valid `source_field` passes; a schema without any new attribute passes (`TestCheckAcceptsSpecExample` stays green).
  - `TestCheckManualOnly`: origin `source`/`adjusted`/`gone` → `fields[i].origin` "only job templates take fields from a playbook"; `manual` and absent pass.
  - `TestFieldJSONKeepsActiveFalse`: `Active: &false` marshals `"active":false`; absent `active` → `IsActive()` true.
  - `TestSameContent`: default `8080` (int) vs `float64(8080)` → true; labels differ → false; only `Active`/`Origin`/`Source.Line` differ → true.
  - `TestSendable`: fields `a` (active), `b` (inactive), `c` (origin gone, active); values `{a,b,c,d}` → `{a,c}`.
  - In `validate_test.go`: `TestValidateIgnoresInactiveFields`: inactive required int `port` with value `"x"` → nil; with `{}` → nil; an active required field missing → "is required"; an undeclared key → "unknown field". Same table for `ValidateStored`.
  - In `resolve_test.go`: `TestResolveSkipsInactiveFields`: an inactive field with a default is neither filled nor kept.
  - `internal/inventory/service_test.go` `TestSetSchemaRefusesSourceFields` → `*schema.ValidationError` at `fields[0].origin`; `internal/shared/service_test.go` `TestTemplateDefinitionRefusesSourceFields` → `ErrInvalidInput`.
- [ ] **Step 2:** run `./internal/inventory/schema/ ./internal/inventory/ ./internal/shared/`, expect failures.
- [ ] **Step 3:** implement. `validateObject` at the top level treats an inactive field's name as declared and skips its value and its `required`. `SetSchema` and `validateTemplateDefinitionInput` call `CheckManualOnly` after `Check` (shared: map to `ErrInvalidInput` with the problem text).
- [ ] **Step 4:** packages pass; `npm run api:gen`, `npx tsc -b` clean.
- [ ] **Step 5: Commit** `feat(schema): give fields an active switch, an origin and a source`.

### Task 2: Parser, part 1: argument specs, `vars_prompt`, inline markers (`internal/varsource`)

**Files:** Create `internal/varsource/doc.go`, `notes.go`, `argspec.go`, `prompt.go`, `marker.go`, `fields.go` and `argspec_test.go`, `prompt_test.go`, `marker_test.go`, `fields_test.go`.

**Interfaces (produced):**
```go
type Level string // LevelWarning "warning", LevelHint "hint"
type Note struct {
	Level   Level  `json:"level"`
	Code    string `json:"code"`
	File    string `json:"file,omitempty"` // relative to the repo subpath
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}
// unexported, used by Task 3:
func argumentSpecFields(data []byte, file, role, entryPoint string, kind schema.SourceKind) (fields []schema.Field, notes []Note, readOK bool, entryFound bool)
func promptFields(prompts *yaml.Node, file string) ([]schema.Field, []Note)
type marker struct{ Enum []string; Secret, Required bool; Label string }
func parseMarker(comment string) (m marker, isMarker bool, err error)
func markerField(key, value *yaml.Node, m marker, file string) (schema.Field, []Note, bool)
func finishField(f schema.Field, file string, line int) (schema.Field, []Note, bool) // name rule, depth 4, 100 fields/level, 100 options, default checked through schema.Check
```
Every produced field has `Origin: source` and a `Source` with kind, role, entry point, file and the YAML line; `Active` is left nil (Task 7 decides activation).

Note codes this task emits (the docs reference lists each; wording of `Message` is free): `invalid_yaml` (warning, one per file), `unsupported_type` (warning: `raw`, `dict` without `options`, `list` without scalar `elements` or dict `options`, `bits`, `bytes`, `jsonarg`, `json`, `sid`, unknown), `choices_converted` (warning), `secret_default_dropped` (warning), `default_invalid` (warning, default dropped), `default_dropped` (warning, default on repeater/object), `no_log_collection` (warning, `no_log` on a list/dict: field skipped), `invalid_name` (warning), `too_deep` (warning), `too_many_fields` (warning), `too_many_options` (warning, field skipped), `entry_point_missing` (hint), `marker_unreadable`, `marker_value_type`, `marker_enum_value` (warnings), `templated_default` (hint), `field_invalid` (warning, any other `schema.Check` problem: field skipped).

Marker grammar (the docs reference copies it):
```ebnf
marker-comment = "#" , { space } , "pulsix:" , [ space1 , body ] , { space } ;
body           = keyword-list , [ space1 , label ] | label ;
keyword-list   = keyword , { space1 , keyword } ;
keyword        = enum | "secret" | "required" ;
enum           = "enum[" , option , { "," , option } , "]" ;
option         = { space } , option-char , { option-char } , { space } ;   (* trimmed *)
option-char    = ? any character except "," , "]" and a line break ? ;
label          = ? the rest of the line, trimmed; the first token that is not a keyword starts it ? ;
space          = " " | "\t" ;   space1 = space , { space } ;
```
Rules: `pulsix:` is lowercase and must open the comment; keywords are lowercase; a repeated keyword, `enum` together with `secret`, an `enum[` without `]`, an empty option list, an empty or duplicate option, or more than 100 options make the marker unreadable (`marker_unreadable`, the variable is then not a field). Only the line comment of a top-level key of a play's `vars:` is a marker (check the key's and the value's `LineComment`).

- [ ] **Step 1: Failing tests** (table-driven, assert type, attributes, `Source.File`/`Line` and note codes with file:line):
  - `TestArgumentSpecTypeMapping`: `str`, `path` → string; `int`; `float`; `bool`; `list`+`elements: str` → list, item string; `list`+`elements: int` → list, item int; `list`+`elements: dict`+`options` → repeater with sub-fields; `dict`+`options` → object; `choices: [a, b]` → enum `[a b]`; `list`+`elements: str`+`choices` → list with item enum; type absent → string; `raw`, `dict` w/o options, `list` w/o elements, `bits`, `bytes`, `jsonarg`, `json`, `sid` → skipped, `unsupported_type`.
  - `TestArgumentSpecAttributes`: `required: true`; int `default: 80`; `description: "Port to listen on\nUsed by nginx."` → label `Port to listen on`, description `Used by nginx.`; `description: [A, B, C]` → label `A`, description `B\nC`; `choices: [1, 2]` → enum `["1","2"]` + `choices_converted`; `no_log: true` + `default: x` → secret, no default, `secret_default_dropped`; `no_log` on a list → skipped, `no_log_collection`; default not among choices → default dropped, `default_invalid`; default on a dict with options → dropped, `default_dropped`.
  - `TestArgumentSpecEntryPoints`: entry point `main` by default; `install` read when asked; asked entry point missing → zero fields, `entryFound` false, hint `entry_point_missing`; invalid YAML → one `invalid_yaml`, `readOK` false.
  - `TestFieldLimits`: `Port` and `ansible_port` → `invalid_name`; five nested dicts → cut at depth 4 with `too_deep`; 101 choices → `too_many_options`; 101 options in one dict → first 100 kept, `too_many_fields`.
  - `TestPromptFields`: `{name: user, prompt: "User name", private: false, default: admin}` → string, label `User name`, default `admin`, not required; `{name: pass, prompt: Password}` → secret, required; `{name: pin, private: true, default: "1234"}` → secret, required, `secret_default_dropped`; a non-private prompt without default → required.
  - `TestParseMarker` (grammar table): `# pulsix: HTTP port` → label; `#pulsix: required HTTP port` → required + label `HTTP port`; `# pulsix: enum[a, b c] required Mode` → options `[a "b c"]`, required, label `Mode`; `# pulsix:` → valid, empty; `# pulsix: secrets Pw` → label `secrets Pw`; `# Pulsix: x` and `# note` → not markers; unreadable: `enum[a`, `enum[]`, `enum[a,,b]`, `enum[a,a]`, `required required`, `enum[a] secret`.
  - `TestMarkerField`: `http_port: 8080  # pulsix: required HTTP port` → int, default 8080, required, label `HTTP port`, kind marker, line 3; `mode: blue  # pulsix: enum[blue,green]` → enum, default `blue`; `mode: red  # pulsix: enum[blue,green]` → skipped, `marker_enum_value`; `token: abc  # pulsix: secret API token` → secret, no default; `ratio: 0.5` → float; `debug: false` → bool; `hosts: [a, b]` → list of string, default `[a b]`; `[]` → list of string, default `[]`; `[1, a]` and `{a: 1}` → `marker_value_type`; `empty:  # pulsix: Label` → string without default; `greeting: "{{ x }}"  # pulsix:` → string without default, hint `templated_default`; an alias value resolves to its anchor.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/varsource/`, expect failures.
- [ ] **Step 3:** implement. `finishField` runs `schema.Schema{Fields: []schema.Field{f}}.Check()`: a default problem drops the default (`default_invalid`), any other problem skips the field (`field_invalid`).
- [ ] **Step 4:** passes.
- [ ] **Step 5: Commit** `feat(varsource): map argument specs, prompts and inline markers onto fields`.

### Task 3: Parser, part 2: playbooks, roles, imports, discovery, caps

**Files:** Create `internal/varsource/playbook.go`, `roles.go`, `imports.go`, `discover.go`, `report.go`, `inline.go`, tests `playbook_test.go`, `discover_test.go`, `report_test.go`, `inline_test.go`, fixture trees `internal/varsource/testdata/basic/…` and `testdata/imports/…`. Modify `internal/playbooks/service.go` (`RepoPathPattern = varsource.PlaybookPathPattern`).

**Interfaces (produced):**
```go
type RoleState string // "read", "no_specs", "not_found", "unreadable", "dynamic", "collection", "inline"
type RoleUse struct{ Name string `json:"name"`; EntryPoint string `json:"entry_point"`; State RoleState `json:"state"` }
type FileState string // "read", "missing", "unreadable", "dynamic", "cycle", "too_deep", "outside"
type FileUse struct{ Path string `json:"path"`; State FileState `json:"state"` } // playbook files and present sidecars
type Playbook struct {
	Path          string         `json:"path"`
	Failed        bool           `json:"failed"` // the playbook file itself was not read
	Fields        []schema.Field `json:"fields"` // precedence order, first wins
	SetInPlaybook []string       `json:"set_in_playbook"`
	Roles         []RoleUse      `json:"roles"`
	Files         []FileUse      `json:"files"`
	Notes         []Note         `json:"notes"`
}
type Report struct {
	Commit     string     `json:"commit"`
	Playbooks  []Playbook `json:"playbooks"`
	Incomplete bool       `json:"incomplete"`
	LeftOut    []string   `json:"left_out,omitempty"`
	Notes      []Note     `json:"notes"`
}
const (MaxPlaybooks = 1000; MaxFileBytes = 512 << 10; MaxImportDepth = 3; MaxReportBytes = 8 << 20)
var PlaybookPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}\.ya?ml$`)
func ParsePlaybook(fsys fs.FS, path string) Playbook
func ParseRepo(fsys fs.FS, commit string) Report
func ParseInline(text string) Playbook // Path "inline"; roles get state "inline", imports "dynamic"
func Encode(r Report, maxBytes int) ([]byte, Report, error)
```
Rules the tests pin:
- A playbook is a YAML file whose first document is a non-empty sequence of mappings, each with `hosts` or `import_playbook` / `ansible.builtin.import_playbook`.
- Discovery walks `fsys` (the repo subpath) in lexical order and skips directories named `roles`, `group_vars`, `host_vars`, `collections`, any directory starting with `.` or ending in `.galaxy`, files named `requirements.y[a]ml` and `*.argument_specs.y[a]ml`. A path failing `PlaybookPathPattern` or containing `..` → `path_unsupported`; a file over 512 KiB → `file_too_large`; an invalid YAML file → `invalid_yaml`. After 1 000 playbooks the rest are left out: `Incomplete`, `LeftOut` lists them, one `too_many_playbooks` warning naming them.
- Roles: `roles:` entries (string, or mapping with `role`/`name`), and tasks in `pre_tasks`, `tasks`, `post_tasks`, `handlers`, recursively through `block`/`rescue`/`always`, using `import_role`/`include_role` (also `ansible.builtin.` FQCN) with a static `name` and optional `tasks_from` (entry point = its value without `.yml`/`.yaml`). Lookup: `<playbook dir>/roles/<name>/meta/argument_specs.y[a]ml`, then `roles/<name>/…` at the subpath root; a name with `/` is a path relative to the playbook's directory and must stay inside `fsys`. `{{` in the name → `dynamic` + hint `role_dynamic`; a dotted name → `collection` + hint `role_collection`; not found → `not_found` + warning `role_not_found`; found without specs → `no_specs` + hint `role_without_specs`. A role+entry point used twice is read once.
- Imports: `import_playbook` paths are relative to the importing file, inside `fsys`; depth 3 below the playbook; cycle → `cycle` + `import_cycle`; deeper → `too_deep` + `import_too_deep`; outside → `outside` + `import_outside`; missing → `missing` + `import_not_found`; `{{` → `dynamic` + hint `import_dynamic`.
- Precedence (first wins, the loser becomes hint `duplicate_name` naming both places): the playbook's own sidecar, its markers, its `vars_prompt` entries, its roles in first-use order; then each imported playbook in import order with the same inner order.
- Set in playbook (union over the playbook and its imports): keys of play `vars:` without a marker, role parameters of `roles:` entries (every key except the role keywords `role name tags when vars become become_user become_method become_flags delegate_to delegate_facts environment any_errors_fatal ignore_errors ignore_unreachable no_log run_once check_mode diff collections connection debugger module_defaults port remote_user throttle timeout apply`), keys of a role entry's `vars:`, keys of `vars:` on an `import_role`/`include_role` task. A name that has a marker anywhere is never set-in-playbook.
- A `# pulsix:` comment anywhere else → warning `marker_misplaced`.
- `Encode` drops playbooks from the end until the JSON fits, sets `Incomplete`, appends to `LeftOut` and adds `report_too_large` naming them.
- `ParseInline` adds hint `inline_roles_unresolved` per role and `inline_import_unresolved` per import.

- [ ] **Step 1: Failing tests** over the fixtures:
  - `TestParsePlaybookBasic` (`testdata/basic/site.yml`: play vars `http_port: 8080  # pulsix: required HTTP port` and plain `app_env: prod`; `vars_prompt: [admin_user]`; `roles: [nginx, {role: app, app_port: 9000}]`; tasks `import_role: {name: db, tasks_from: install}` and `include_role: {name: "{{ role_name }}"}`; sidecar `site.argument_specs.yml` with `release` str required; role specs: `nginx` main = `nginx_port` int default 80, `nginx_mode` choices `[blue, green]`, `app_env` str required; `app` main = `app_port` int, `app_secret` str `no_log`; `db` entry point `install` = `db_path` str): `Fields` names in order `release, http_port, admin_user, nginx_port, nginx_mode, app_env, app_port, app_secret, db_path`; `app_env` and `app_port` have `SetInPlaybook`; `http_port` does not; `app_secret` (no_log) is secret; `Roles` = nginx read, app read, db/install read, `{{ role_name }}` dynamic with hint `role_dynamic` at its line; `Files` = `site.yml` read, `site.argument_specs.yml` read.
  - `TestPrecedenceFirstWins`: `release` declared by the sidecar and the nginx role → sidecar version kept, one `duplicate_name` hint naming `roles/nginx/meta/argument_specs.yml` and its line.
  - `TestRoleResolution`: `sub/roles/local` wins over `roles/local`; `roles/shared` found from `sub/play.yml`; missing role, dotted collection role, role without specs, FQCN `ansible.builtin.include_role` inside `block`/`rescue`/`always`, `pre_tasks`, `post_tasks`, `handlers` — each with the state and code above.
  - `TestImports` (`testdata/imports`): `a.yml` imports `b.yml` → b's fields after a's; `b.yml` importing `a.yml` → `cycle`; chain `d1…d5` → `d4` read, `d5` `too_deep`; `../x.yml` → `outside`; `"{{ x }}.yml"` → `dynamic`; `nope.yml` → `missing`; a var set in `b.yml`'s play vars marks the role field from `a.yml` set-in-playbook.
  - `TestParsePlaybookFailures`: invalid YAML → `Failed`, exactly one `invalid_yaml`; a 600 KiB playbook → `Failed`, `file_too_large`.
  - `TestMarkerMisplaced`: a marker on a task's `vars:` key → `marker_misplaced` with its line, no field.
  - `TestDiscover` (`ParseRepo` over `testdata/basic`): playbooks `[site.yml, sub/play.yml]`; nothing from `roles/`, `group_vars/`, `host_vars/`, `collections/`, `.hidden/`, `x.galaxy/`, sidecars, `requirements.yml`; `notes.yml` (a mapping) silently ignored; `bad.yml` → `invalid_yaml` in `Report.Notes`; `with space.yml` → `path_unsupported`.
  - `TestParseRepoCapsPlaybooks` (`fstest.MapFS` with 1 001 playbooks): 1 000 parsed, `Incomplete`, `LeftOut` = the 1 001st path, `too_many_playbooks` names it.
  - `TestEncodeFitsTheCap`: under the cap unchanged; with a small `maxBytes` the last playbooks are dropped, named in `LeftOut` and `report_too_large`; the result decodes.
  - `TestParseInline`: marker and prompt fields read; a `roles:` entry → state `inline`, hint `inline_roles_unresolved`; `Path` `inline`.
- [ ] **Step 2:** run, expect failures.
- [ ] **Step 3:** implement; keep each file under ~400 lines.
- [ ] **Step 4:** `./internal/varsource/`, `./internal/playbooks/` pass.
- [ ] **Step 5: Commit** `feat(varsource): read a playbook's variable sources across roles, imports and the repo`.

### Task 4: The worker reads the sources during a repo sync

**Files:** Create `internal/worker/sync_sources.go`. Modify `internal/worker/sync.go` (`RepoFetcher`, `Run`), `internal/worker/runner/gitrepo.go` (`HeadCommit`), tests `internal/worker/sync_test.go` (fake fetcher gains `HeadCommit`), `internal/worker/runner/gitrepo_test.go`, `internal/worker/system_e2e_test.go`.

**Interfaces:** Consumes `varsource.ParseRepo`, `varsource.Encode`, `varsource.MaxReportBytes`. Produces: `RepoFetcher` gains `HeadCommit(ctx context.Context, dir string) (string, error)` (runner: `git rev-parse HEAD` in the clone, trimmed, URL-redacted diagnostics); `const VarSourcesStatsKey = "var_sources"` — the stats value is the encoded report as `json.RawMessage`; the ok-event payload gains only a summary `"var_sources": {"commit", "playbooks", "warnings"}`.

- [ ] **Step 1: Failing tests:**
  - `TestSyncExecutorReportsVarSources`: clone dir with `site.yml` (role `web`) and `roles/web/meta/argument_specs.yml` (`web_port` int), `HeadCommit` → `9f8e7d6c5b4a39281706f5e4d3c2b1a098765432`; `stats["var_sources"]` decodes to a `Report` with that commit and one playbook `site.yml` with field `web_port`; the event payload carries the summary `{commit, playbooks: 1, warnings: 0}` and no `playbooks` array.
  - `TestSyncExecutorReadsSourcesUnderTheSubpath`: subpath `ansible` → paths relative to it, a playbook outside it not listed.
  - `TestSyncExecutorSourcesNeverFailTheSync`: an invalid YAML playbook and a missing role → `StatusSucceeded`, notes present; a panic inside parsing (inject through an unexported seam `parseRepo` variable on the executor) → succeeded, report with one `read_failed` note.
  - `TestSyncExecutorDoesNotFollowSymlinksOutOfTheClone`: `roles/x/meta/argument_specs.yml` is a symlink to a valid spec outside the clone → role state `unreadable`, no field from it.
  - The existing fetch-failure test also asserts no `var_sources` key.
  - runner `TestHeadCommit`: against a local repo returns `git rev-parse HEAD`; a non-repo dir → error.
  - `system_e2e_test.go`: the real-fetch sync's `var_sources.commit` equals the fixture repo's HEAD.
- [ ] **Step 2:** run `./internal/worker/ ./internal/worker/runner/`, expect failures.
- [ ] **Step 3:** implement: after requirements, open `os.OpenRoot(filepath.Join(dir, subpath))` and parse `root.FS()` (escaping symlinks fail to open → `unreadable`); a `HeadCommit` error leaves the commit empty with a `read_failed` note; `Encode(r, varsource.MaxReportBytes)`; `recover` around the read.
- [ ] **Step 4:** passes.
- [ ] **Step 5: Commit** `feat(worker): read playbook variable sources during a repo sync`.

### Task 5: The manager stores what a sync read (migration 00081)

**Files:** Create `internal/db/migrations/00081_variable_sources.sql`, `internal/db/queries/var_sources.sql`, `internal/repos/sources.go`, `internal/repos/sources_store_sql.go`, tests `internal/repos/sources_test.go`, `internal/repos/sources_store_sql_test.go`. Modify `internal/repos/service.go` (`Store` interface), `internal/jobs/ports.go` (`SourceSink`, `SourceApplier`), `internal/jobs/queue_sql.go` (`finish`, `recordSync`, setters), `internal/jobs/terminal.go` (`terminalTransition.varSources`, post-commit hook), `internal/app/app.go` (`jobsQueue.SetSourceSink(reposSvc)`), `internal/backup/fixture_test.go` (one row per new table), `internal/db/gen/*` (regenerated), `internal/jobs/queue_sql_test.go`, `docs/upgrades.md` (one line: an older worker reports no variable sources; the record is kept and flagged).

Migration (Down drops the columns and tables):
```sql
CREATE TABLE repo_var_sources (
  repo_id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  commit_sha text NOT NULL DEFAULT '', synced_at timestamptz NOT NULL,
  worker_reads boolean NOT NULL, incomplete boolean NOT NULL DEFAULT false,
  left_out jsonb NOT NULL DEFAULT '[]', notes jsonb NOT NULL DEFAULT '[]',
  FOREIGN KEY (org_id, repo_id) REFERENCES git_repos (org_id, id) ON DELETE CASCADE);
CREATE TABLE repo_playbook_sources (
  org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  repo_id uuid NOT NULL, path text NOT NULL, result jsonb NOT NULL,
  PRIMARY KEY (repo_id, path),
  FOREIGN KEY (org_id, repo_id) REFERENCES git_repos (org_id, id) ON DELETE CASCADE);
-- ENABLE + FORCE ROW LEVEL SECURITY and the org_isolation / org_isolation_bypass pair on both, as 00067.
ALTER TABLE playbooks ADD COLUMN var_source jsonb;
ALTER TABLE job_templates
  ADD COLUMN revision bigint NOT NULL DEFAULT 1,
  ADD COLUMN vars_mode text NOT NULL DEFAULT 'off' CHECK (vars_mode IN ('off','manual','automatic')),
  ADD COLUMN vars_source_rev text NOT NULL DEFAULT '',
  ADD COLUMN vars_applied_at timestamptz;
```
Queries: `UpsertRepoVarSources :exec` (worker_reads true), `MarkRepoVarSourcesUnread :exec` (insert with `commit_sha ''`, `synced_at = @at`, or on conflict only `worker_reads = false`), `DeleteRepoPlaybookSources :exec`, `InsertRepoPlaybookSource :exec`, `GetRepoVarSources :one`, `ListRepoPlaybookSources :many` (by path), `GetRepoPlaybookSource :one`.

**Interfaces (produced):**
```go
// internal/repos
type SourcesRecord struct { Commit string; SyncedAt time.Time; WorkerReads bool; Incomplete bool; LeftOut []string; Notes []varsource.Note }
func (s *Service) RecordVarSources(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, raw json.RawMessage) error // nil raw = an older worker
func (s *Service) VarSources(ctx context.Context, orgID, repoID uuid.UUID) (SourcesRecord, []varsource.Playbook, bool, error) // bool: a record exists
func (s *Service) PlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (SourcesRecord, varsource.Playbook, bool, error)
// Store gains ReplaceVarSources(ctx, orgID, repoID, SourcesRecord, []varsource.Playbook) error,
//   MarkVarSourcesUnread(ctx, orgID, repoID, at) error, GetVarSources, ListPlaybookSources, GetPlaybookSource
// internal/jobs
type SourceSink interface { RecordVarSources(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, raw json.RawMessage) error }
type SourceApplier interface { ApplyRepoSources(ctx context.Context, orgID, repoID uuid.UUID) error }
func (q *SQLQueue) SetSourceSink(s SourceSink)
func (q *SQLQueue) SetSourceApplier(a SourceApplier) // wired in Task 9
```

- [ ] **Step 1: Failing tests:**
  - DB `TestReplaceVarSourcesReplacesThePreviousRecord`: record A (commit `a1b2c3d…`, 2 playbooks) then B (commit `9f8e7d6…`, 1 playbook) → only B's playbook and commit; another org reads nothing.
  - DB `TestMarkVarSourcesUnreadKeepsTheRecord`: after A, mark unread → commit and playbooks kept, `worker_reads` false; with no prior record → a row with empty commit, `worker_reads` false, no playbooks.
  - Service `TestRecordVarSources` (fake store): nil raw → `MarkVarSourcesUnread`; malformed raw → nothing written, nil error, one log line; valid → `ReplaceVarSources` with the decoded playbooks.
  - Jobs (DB) `TestFinishSyncStripsVarSourcesFromStats`: `FinishFor` with stats `{repo_id, requirements_installed, var_sources: {...}}` → stored `jobs.stats` has no `var_sources`; the sink received the report JSON. `TestFinishSyncWithoutVarSourcesMarksUnread`: sink called with nil. A failed or cancelled sync and a non-sync job → sink not called. `TestFinishSyncAppliesSourcesAfterCommit`: the applier is called once with org and repo, sees the committed `repo_var_sources` row through a fresh connection, and an applier error leaves the job `succeeded`.
  - The backup round trip (`internal/backup`) and `internal/db` RLS/migration tests pass with the new tables.
- [ ] **Step 2:** run, expect failures.
- [ ] **Step 3:** implement. `finish` reads `kind` with `org_id, zone_id, worker_id`; for `sync_repo` it removes `var_sources` from the stats map (re-encoded to `json.RawMessage`) before `statsJSON`. `recordSync` calls the sink only on `StatusSucceeded`, inside the transaction; a gone repo is logged and skipped like today. After commit (`applied`), for a succeeded sync that sent sources, call the applier with `context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)` and log its error.
- [ ] **Step 4:** `./internal/repos/ ./internal/jobs/ ./internal/backup/ ./internal/db/` pass; sqlc diff clean.
- [ ] **Step 5: Commit** `feat(repos,jobs): store the variable sources a repo sync read`.

### Task 6: Discovered playbooks, picking one, inline sources

**Files:** Create `internal/playbooks/varsource.go` (+ `varsource_test.go`), `internal/api/repo_playbook_handlers.go` (+ `_test.go`). Modify `internal/playbooks/service.go` (`RepoSource` port, `CreateEditor`/`UpdateEditor`), `internal/playbooks/store_sql.go`, `internal/db/queries/playbooks.sql` (`FindRepoPlaybook :one` ordered by `created_at, id`; `CreatePlaybook` and `UpdatePlaybookInline` write `var_source`), `internal/db/gen/*`, `internal/app/app.go`, `internal/api/api.go` (route registration).

**Interfaces (produced):**
```go
type VarSource struct {
	Kind        string     // "repo" | "inline"
	Rev         string     // repo: commit; inline: "text:" + first 12 hex chars of sha256(inline)
	SyncedAt    *time.Time // repo only
	WorkerReads bool       // inline: true
	Incomplete  bool
	Found       bool       // a parsed result exists for this playbook
	RepoID      *uuid.UUID
	Path        string
	Playbook    varsource.Playbook
}
type DiscoveredPlaybook struct { Path string; Fields, Warnings int; Failed bool; PlaybookID *uuid.UUID }
type Discovery struct { Record *repos.SourcesRecord; Playbooks []DiscoveredPlaybook }
func (s *Service) VarSource(ctx context.Context, orgID, playbookID uuid.UUID) (VarSource, error)
func (s *Service) Discovered(ctx context.Context, orgID, repoID uuid.UUID) (Discovery, error)
func (s *Service) EnsureRepo(ctx context.Context, orgID, repoID uuid.UUID, path string) (Playbook, bool, error) // bool: created
type InlineApplier interface { ApplyPlaybookSources(ctx context.Context, orgID, playbookID uuid.UUID) error }
func (s *Service) SetInlineApplier(a InlineApplier) // wired in Task 9 (templates depends on playbooks)
```
`RepoSource` gains `VarSources` and `PlaybookVarSource` (Task 5's methods). `EnsureRepo` runs `FindRepoPlaybook` and `CreateRepo` in one write transaction under `pg_advisory_xact_lock(hashtext('repo-playbook:' || repo_id || ':' || path))` (no unique index exists and one cannot be added additively). An inline playbook whose `var_source` is NULL (saved before 00081) is parsed on read, not written.

API:
- `GET /api/v1/orgs/{org}/repos/{id}/playbooks` (`repos.read`) → `{sources: {commit, synced_at, worker_reads, incomplete, left_out, notes} | null, playbooks: [{path, fields, warnings, failed, playbook_id?}]}`.
- `POST /api/v1/orgs/{org}/repos/{id}/playbooks` (`playbooks.write`, audit `pickRepoPlaybook`, object `playbook`) body `{path}` → `{playbook, created}`.

- [ ] **Step 1: Failing tests:**
  - `TestCreateEditorStoresItsVarSource`: inline with marker `http_port: 8080  # pulsix: required HTTP port` → `VarSource` Kind `inline`, Found, field `http_port`; `Rev` stable for the same text, different after an edit.
  - `TestUpdateEditorReparsesAndApplies`: the applier is called once with the playbook id; an applier error is logged, the save still succeeds.
  - `TestVarSourceParsesALegacyInlinePlaybook`: `var_source` NULL → parsed result returned.
  - `TestVarSourceRepoPlaybook`: record present with the path → Found, Rev = commit, SyncedAt set; path missing from the record → Found false; no record → Found false, WorkerReads false.
  - DB `TestEnsureRepoReusesTheRow`: two calls → same id, `created` true then false; two concurrent calls → one row.
  - API `TestRepoPlaybooksDiscoveredList`: seeded record → paths, counts, `playbook_id` only for a registered path; viewer 200; another org 404. `TestPickRepoPlaybook`: operator 200 `created: true`, again `created: false` with the same id; viewer 403; path `../x.yml` 400; unknown repo 404.
- [ ] **Step 2:** run, expect failures. **Step 3:** implement. **Step 4:** `./internal/playbooks/` and `./internal/api/` (1800 s) pass; sqlc diff clean; `npm run api:gen`.
- [ ] **Step 5: Commit** `feat(playbooks,api): list a repo's discovered playbooks and pick one`.

### Task 7: The preview, as a pure function (`internal/varsync`)

**Files:** Create `internal/varsync/preview.go`, `diff.go`, `gone.go`, `apply.go`, tests `preview_test.go`, `gone_test.go`, `apply_test.go`.

**Interfaces (produced):**
```go
type Source struct { Rev string; Available bool; Playbook varsource.Playbook; Reason string } // Available false: no record, not found, Failed, left out, worker_reads false
type Change struct { Attr string `json:"attr"`; Old any `json:"old,omitempty"`; New any `json:"new,omitempty"` }
type ReviewReason string // "type", "secret", "value"
type Added struct { Field schema.Field `json:"field"` }
type Changed struct { Name string `json:"name"`; Field schema.Field `json:"field"`; Changes []Change `json:"changes"`; NeedsReview bool `json:"needs_review"`; Reason ReviewReason `json:"reason,omitempty"` }
type Adjusted struct { Name string `json:"name"`; Field schema.Field `json:"field"`; SourceField schema.Field `json:"source_field"`; Changes []Change `json:"changes"` }
type Gone struct { Name string `json:"name"`; Field schema.Field `json:"field"` }
type Preview struct { SourceRev string `json:"source_rev"`; Added []Added `json:"added"`; Changed []Changed `json:"changed"`; Adjusted []Adjusted `json:"adjusted"`; Gone []Gone `json:"gone"`; Unchanged int `json:"unchanged"`; Notes []varsource.Note `json:"notes"` }
type ApplyOptions struct { Automatic bool; RemoveGone bool }
func Compute(current []schema.Field, src Source, stored map[string]any) Preview
func Apply(current []schema.Field, p Preview, opt ApplyOptions) []schema.Field
```
Rules:
- Added: candidate name not in `current`; `Field.Active = required && default == nil && !set_in_playbook`.
- A `manual` field with a candidate's name is never touched; hint `manual_name_taken`.
- `source` field whose content (`SameContent`) differs → Changed (new content, current `Active`); same content → unchanged (Source ref and `set_in_playbook` refreshed on apply).
- `adjusted` field whose `source_field` differs from the candidate → Adjusted (adjustment kept, new `source_field`); else unchanged. Sub-fields are part of the top-level unit (attr `fields`), never merged.
- `gone` field whose name is back → Changed to `source` with the candidate's content, or to `adjusted` with `source_field` = candidate when it had a `source_field`.
- NeedsReview: type differs (`string`↔`secret` → `secret`, else `type`); else a stored value exists and `schema.ValidateStored([]Field{new}, {name: v})` fails → `value`.
- Gone (only for `source`/`adjusted`; `gone` fields are listed again): src not Available → nothing gone, warning `source_unavailable` with the reason. Kind `argument_spec`: the field's role+entry point has a `RoleUse` in state `read`/`no_specs` → gone; in any other state → kept; no `RoleUse` → gone unless any role is `dynamic`. Kinds `marker`/`vars_prompt`/`sidecar`: its file has a `FileUse` `read` → gone; other state → kept; no `FileUse` → gone unless any file is `dynamic`. Kept fields add one warning `not_checked` with their count.
- Apply: existing order kept, Added appended in candidate order; `Active` of an existing field never changes. Automatic: applies Added, Changed without NeedsReview, Adjusted, marks Gone (`origin: gone`, `source_field` kept); never removes. Person: applies everything; removes gone fields only with `RemoveGone`.
- Diff attrs: `label, description, type, required, default, options, min, max, pattern, min_items, max_items, item, fields`.

- [ ] **Step 1: Failing tests:**
  - `TestComputeFirstLink`: empty current; candidates `release` (required, no default), `nginx_port` (int, default 80), `app_env` (required, set in playbook) → Added, active `[true, false, false]`.
  - `TestComputeChanged`: `nginx_port` default 80 → 8080 → Changed `[{default 80 8080}]`, no review.
  - `TestReviewReasons` (table): int→string `type`; string→secret `secret`; enum loses `blue` with stored `{"mode":"blue"}` → `value`; enum loses an option nobody stored → none; `max` 100→10 with stored 50 → `value`; tighter `pattern`, nothing stored → none.
  - `TestComputeAdjusted`: adjusted label `Port`, `source_field` default 80, candidate default 8080 → Adjusted; Apply keeps label `Port`, `source_field.default` 8080.
  - `TestComputeGoneOnlyFromReadSources` (table, Review Focus 1): role read + name absent → gone; role `not_found`/`unreadable` → kept + `not_checked`; role no longer used, no dynamic → gone; role no longer used, a dynamic role present → kept; marker field, playbook file read → gone; file `unreadable`/`missing`/`cycle` → kept; sidecar no longer present → gone; src Failed / not found / left out / worker without sources → nothing gone, `source_unavailable`.
  - `TestComputeReturningField`: gone without `source_field` → Changed to `source`; with → `adjusted`.
  - `TestComputeLeavesManualFields`: manual `release` vs candidate `release` → untouched, hint `manual_name_taken`.
  - `TestComputeSourceRefMoveIsUnchanged`: only `source.line` differs → `Unchanged` 1; Apply updates the line.
  - `TestSubFieldsAreOneUnit`: object sub-field type change → Changed with attr `fields`; adjusted object with a manual sub-field and a source sub-field change → Adjusted, manual sub-field kept.
  - `TestApplyAutomaticHoldsReviewAndRemovals`: added + safe change + review change + gone, `RemoveGone: true` → added present, safe change applied, review change not, gone field kept as `gone`.
  - `TestApplyPersonRemovesGoneOnlyWhenAsked`; `TestApplyNeverChangesActive` (an inactive source field that changed stays inactive).
- [ ] **Step 2:** run, expect failures. **Step 3:** implement. **Step 4:** `./internal/varsync/` passes.
- [ ] **Step 5: Commit** `feat(varsync): compute and apply a template's source preview`.

### Task 8: Template revisions, a variables endpoint, runs send active fields only

**Files:** Create `internal/templates/variables.go` (+ `variables_test.go`), `internal/api/template_variables_handlers.go` (+ `_test.go`), `internal/jobs/sendable.go`. Modify `internal/db/queries/templates.sql` (`UpdateTemplate`: `AND (sqlc.narg(if_revision)::bigint IS NULL OR revision = sqlc.narg(if_revision))`, `revision = revision + 1`; new `UpdateTemplateVariables :one` writing schema, vars, `vars_mode`, `vars_source_rev`, `vars_applied_at` with the same guard), `internal/db/queries/shared.sql` (`UpdateProjectedTemplate` ~390 and `ResolveTemplatePlaybooksForNewPath` ~436 bump `revision`), `internal/templates/service.go` (`Template`, `UpdateInput`, `build`), `internal/templates/store_sql.go`, `internal/templates/overrides.go` (~line 92), `internal/jobs/queue_sql.go` (~lines 522-553), `internal/api/template_handlers.go` (DTO, ETag, If-Match, 412), `internal/db/gen/*`, tests in `internal/templates/`, `internal/jobs/queue_payload_test.go`, `internal/api/template_handlers_test.go`, `internal/shared/store_sql_test.go`.

**Interfaces (produced):**
```go
var ErrRevisionMismatch = errors.New("revision mismatch")
type SourceMode string // ModeOff "off", ModeManual "manual", ModeAutomatic "automatic"
// Template gains Revision int64; VarsMode SourceMode; VarsSourceRev string; VarsAppliedAt *time.Time
// UpdateInput gains IfRevision *int64; Store.Update(ctx, t Template, ifRevision *int64) (Template, error)
type VariablesInput struct { Schema *schema.Schema; ExtraVars map[string]any; IfRevision *int64 }
func (s *Service) UpdateVariables(ctx context.Context, orgID, id uuid.UUID, in VariablesInput) (Template, error)
// Store gains UpdateVariables(ctx, t Template, ifRevision *int64) (Template, error)
func carryOver(fields []schema.Field, incoming, stored map[string]any) map[string]any
func sendableVars(schemaRaw, vars json.RawMessage) (json.RawMessage, error) // internal/jobs; nil schema → unchanged
```
- `build` validates and seals the incoming vars as today (Validate now ignores inactive fields), then `carryOver` adds every stored key the body omits whose field is inactive or undeclared, verbatim.
- `ValidateOverrides` merges only the stored values of declared fields with the overrides before `schema.Validate`.
- The claim filters both `varsRaw` and `wireRaw` through `sendableVars` before `stringMap`, `checkWireMarkers` and `redact.addExtraVarSecrets`.
- API: `templateDTO` gains `revision`, `vars_mode`; GET one and PUT answer `ETag`; PUT reads `If-Match` with `optionalIfMatch`; `ErrRevisionMismatch` → 412 "this changed since you loaded it; reload and apply your change again". `GET /api/v1/orgs/{org}/job-templates/{id}/variables` (`templates.read`) → `{extra_vars_schema, extra_vars (masked), revision, mode}` + ETag; `PUT …/variables` (`templates.write`, audit `updateTemplateVariables`) body `{extra_vars_schema, extra_vars}`, optional If-Match; a projection → 400 through `refuseSharedFieldChange`.

- [ ] **Step 1: Failing tests:**
  - DB `TestUpdateTemplateHonoursRevision`: IfRevision 1 → revision 2; IfRevision 1 again → `ErrRevisionMismatch`; nil → always; unknown id → `ErrNotFound`. `TestProjectedTemplateUpdateBumpsRevision` (shared store).
  - `TestInactiveFieldKeepsItsStoredValue` (Review Focus 2): secret `token` stored sealed, string `region`; save with `token` inactive and values `{region}` → stored `token` bytes identical; save `token` active with a keep marker → value present (masked `{"$secret":"set"}` on read).
  - `TestRemovedFieldKeepsItsStoredValue`: drop `region` from the schema, values `{}` → stored keeps `region`; add `region` back → read returns it.
  - `TestSaveIgnoresInvalidValuesUnderInactiveFields`: inactive required int with stored `"x"` → save ok.
  - `TestOverridesIgnoreInactiveFields`: override naming an inactive field accepted; a stored value under a removed name does not make overrides 422; inactive required missing → ok; active required missing → 422 "is required".
  - `TestClaimSendsActiveFieldsOnly` (`queue_payload_test.go`): schema `a` active, `b` inactive, `c` gone+active; stored `{a,b,c,d}`, schedule override `{b}` → `Job.extra_vars` keys exactly `{a, c}`; secret under `b` not in `redact`; a template without schema still sends every key.
  - API `TestTemplateIfMatch`: GET ETag `"1"`; PUT `If-Match: "1"` → 200, ETag `"2"`; again `"1"` → 412; no If-Match → 200. `TestTemplateVariablesEndpoint`: GET returns masked values, `mode: "off"`; PUT stale → 412; viewer PUT → 403; projection PUT → 400.
- [ ] **Step 2:** run, expect failures. **Step 3:** implement. **Step 4:** `./internal/templates/ ./internal/jobs/ ./internal/shared/ ./internal/api/` pass; sqlc diff clean; `npm run api:gen`; existing SPA tests still pass (`npx vitest run src/features/templates`).
- [ ] **Step 5: Commit** `feat(templates,jobs): template revisions, a variables endpoint, and runs send active fields only` (API may be its own `feat(api): …` commit).

### Task 9: Link, mode, preview, apply, and the automatic apply

**Files:** Create `internal/templates/varsource.go` (+ `varsource_test.go`), `internal/templates/autoapply.go` (+ `autoapply_test.go`). Modify `internal/templates/service.go` (`PlaybookLookup` gains `VarSource(ctx, orgID, id uuid.UUID) (playbooks.VarSource, error)`; test fakes too), `internal/db/queries/templates.sql` (`ListAutomaticTemplatesForRepo :many` joining `playbooks` on `repo_id`; `ListAutomaticTemplatesForPlaybook :many`; `SetTemplateVarsMode :one` with the revision guard), `internal/templates/store_sql.go`, `internal/api/template_variables_handlers.go`, `internal/api/template_handlers.go` (list DTO `variables_review`), `internal/app/app.go` (`jobsQueue.SetSourceApplier(templatesSvc)`, `playbooksSvc.SetInlineApplier(templatesSvc)`), `internal/db/gen/*`, API tests.

**Interfaces (produced):**
```go
type SourceInfo struct { Kind string; Rev string; SyncedAt *time.Time; AppliedRev string; Newer bool; WorkerReads bool; Incomplete bool; Found bool; RepoID *uuid.UUID; Path string }
type Review struct { Gone int; NeedsReview int; MissingRequired int }
type VariablesView struct { Template Template; Source SourceInfo; Review Review }
var ErrSourceChanged = errors.New("the source changed since the preview")
type ApplyInput struct { Mode SourceMode; SourceRev string; RemoveGone bool; Values map[string]any; IfRevision *int64 }
func (s *Service) Variables(ctx context.Context, orgID, id uuid.UUID) (VariablesView, error)
func (s *Service) SetVariablesMode(ctx context.Context, orgID, id uuid.UUID, mode SourceMode, ifRevision *int64) (Template, error)
func (s *Service) PreviewVariables(ctx context.Context, orgID, id uuid.UUID) (varsync.Preview, SourceInfo, error)
func (s *Service) ApplyVariables(ctx context.Context, orgID, id uuid.UUID, in ApplyInput) (Template, error)
func (s *Service) ApplyRepoSources(ctx context.Context, orgID, repoID uuid.UUID) error         // jobs.SourceApplier
func (s *Service) ApplyPlaybookSources(ctx context.Context, orgID, playbookID uuid.UUID) error // playbooks.InlineApplier
```
Rules:
- `SetVariablesMode`: `off` → `manual`/`automatic` is `ErrInvalidInput` ("link through the preview"); `manual` ↔ `automatic` allowed; any → `off` makes every field manual (clears `origin`, `source`, `source_field`, `set_in_playbook`) and empties `vars_source_rev`. A projection → `ErrForbidden` for mode and apply.
- `PreviewVariables` always answers; `Source.Available` false yields no items and the `source_unavailable` note.
- `ApplyVariables`: `Mode` must be `manual` or `automatic`; recompute; `SourceRev` ≠ current rev → `ErrSourceChanged`; values = stored overlaid with `in.Values` (`null` removes); seal and validate through the `build` path (a value that no longer fits → `*schema.ValidationError`, mapped to `body.values.<name>`); writes fields, mode, `vars_source_rev`, `vars_applied_at`, revision + 1.
- Automatic (`ApplyRepoSources`/`ApplyPlaybookSources`): every `automatic` template of the repo's (or the playbook's) playbooks; skip when `!Found` or `!WorkerReads`; skip the write when Apply changes nothing; write fields + `vars_source_rev` with `IfRevision` = the revision read, values verbatim (never seal, unseal or drop); on `ErrRevisionMismatch` re-read and retry once, then log. Errors per template are logged, the loop continues.
- Review: `Gone` = `len(preview.Gone)`; `NeedsReview` = Changed with `NeedsReview`; `MissingRequired` = active, required, no default, no stored value. The template list adds `variables_review` for linked templates (one `VarSource` per distinct playbook).
- API (`internal/api/template_variables_handlers.go`): GET variables gains `source` and `review`; `PUT …/variables/mode` (`templates.write`, audit `setTemplateVariablesMode`, If-Match) body `{mode}`; `POST …/variables/preview` (`templates.write`) → `{preview, source}`; `POST …/variables/apply` (`templates.write`, audit `applyTemplateVariables`, If-Match) body `{mode, source_rev, remove_gone, values}`. `ErrSourceChanged` → 409 "the source changed since the preview; preview again".

- [ ] **Step 1: Failing tests:**
  - `TestLinkThroughPreview`: off template; `SetVariablesMode(manual)` → `ErrInvalidInput`; preview → all Added; apply `{manual, rev}` → origins `source`, activation per Task 7, mode `manual`, `vars_source_rev` = rev, revision + 1.
  - `TestApplyRefusesAChangedSource` → `ErrSourceChanged`.
  - `TestApplyRequiresReEnteredValues`: `port` string→int with stored `"eighty"` → problem `port`; with `Values {"port": 80}` ok; secret→string with a sealed value → problem until re-entered; string→secret with stored `"abc"` → stored sealed after apply, read masked.
  - `TestUnlinkMakesFieldsManual`.
  - `TestAutomaticApplyAfterSync`: added optional + added required + safe change + type change + gone → optional inactive, required active, safe change applied, type change held, gone marked `gone`, revision bumped, stored values byte-identical; `Variables` review `{Gone: 1, NeedsReview: 1, MissingRequired: 1}`.
  - `TestAutomaticApplyNeverSealsOrUnseals`: string→secret held, plaintext untouched.
  - `TestAutomaticApplySkipsManualTemplatesAndOldWorkers` (Review Focus 3); `TestAutomaticApplyIsIdempotent` (second call writes nothing, revision unchanged); `TestAutomaticApplyLosesNoConcurrentSave` (store fake answers `ErrRevisionMismatch` once → re-read, the person's field kept, source change applied) (Review Focus 4); `TestInlineSaveAppliesAutomaticTemplates`; `TestProjectedTemplateCannotLink` → `ErrForbidden`.
  - API `TestTemplateVariablesLinkFlow`: seed a repo record, pick the path, create a template, preview → added names; apply with `If-Match: "1"` → 200, ETag `"2"`; GET → `source.commit`, `applied_rev`, `newer: false`; seed a newer record → `newer: true`; mode `automatic` → 200; mode on an unlinked template → 400; stale `source_rev` → 409; viewer preview → 403.
- [ ] **Step 2:** run, expect failures. **Step 3:** implement. **Step 4:** `./internal/templates/ ./internal/api/ ./internal/app/` pass; sqlc diff clean; `npm run api:gen`.
- [ ] **Step 5: Commit** `feat(templates,api): link a template's variables to its playbook, preview and apply`.

### Task 10: SPA: the `variables` feature and the full-width editor

**Files:** `git mv web/src/features/inventory/schema web/src/features/variables` (every file), then update importers: `web/src/features/inventory/tabs/{SchemaTab,ValuesTab,AssignHostDialog}.tsx`, `web/src/features/instance/shared/SharedTemplateFormPage.tsx`, `web/src/features/templates/{TemplateDialog.tsx,vars.ts,vars.test.ts}`, `web/src/api/hooks/useInventory.ts`. Create in `web/src/features/variables/`: `VariableEditor.tsx`, `FieldRow.tsx`, `SubFieldList.tsx`, `FieldDialog.tsx`, `FieldAttributes.tsx` (from `SchemaBuilder`'s `FieldEditor`), `inputs.tsx` (`TextField`, `NumberField`, `OptionsField`, `SwitchField`), `AssignmentPreview.tsx`, `OriginPill.tsx`, `origin.ts`, `usage.ts` (types only here), tests `VariableEditor.test.tsx` (absorbs `SchemaBuilder.test.tsx`), `FieldDialog.test.tsx`, `origin.test.ts`. Delete `SchemaBuilder.tsx`. Modify `types.ts`, `SchemaForm.tsx`, `SchemaForm.test.tsx`. Create `web/src/i18n/{en,de}/variables.ts` (the `builder` and `form` keys move there from `inventory.ts`, plus the new keys), register in both `index.ts`.

**Interfaces (produced):**
```ts
export type FieldOrigin = 'manual' | 'source' | 'adjusted' | 'gone';
export interface FieldSource { kind: 'argument_spec' | 'vars_prompt' | 'marker' | 'sidecar'; role?: string; entry_point?: string; file: string; line?: number }
// SchemaField gains: active?: boolean; origin?: FieldOrigin; source?: FieldSource; set_in_playbook?: boolean; source_field?: SchemaField
export function isActive(f: SchemaField): boolean
export function originOf(f: SchemaField): FieldOrigin
export function editLocked(prev: SchemaField, next: SchemaField): SchemaField // source + content change → adjusted, source_field = prev content
export function resetToSource(f: SchemaField): SchemaField                   // content = source_field, origin source, active kept
export function keepAsManual(f: SchemaField): SchemaField                    // gone → manual, source dropped
export function activeFields(fields: readonly SchemaField[]): SchemaField[]
export interface UsageEntry { kind: 'schedule' | 'webhook'; id: string; name: string }
export type FieldUsage = Record<string, readonly UsageEntry[]>;
export interface VariableEditorProps { value: VarSchema; onChange: (next: VarSchema) => void; problems?: readonly Problem[]; variant: 'group' | 'template' | 'shared'; usage?: FieldUsage; readOnly?: boolean }
```
Behaviour: a full-width table (Name, Type, Required, Default, Origin — template variant only, Active switch); "+ Field" opens `FieldDialog` for a new manual field; a row opens `FieldDialog`; a `repeater`/`object` row expands and edits its sub-fields inline (`SubFieldList`); editing a sub-field of a `source` field makes the top-level field `adjusted`; "Add sub-field" is disabled while the field is `source`. `FieldDialog`: name read-only unless manual; source line "From <kind> · <file>:<line>"; for `adjusted` "Source says: <value>" per differing attribute and "Reset to source"; for `gone` "Remove" and "Keep as manual field"; group variant shows scope/overridable and keeps the assignment preview below the table; shared variant offers no `secret` type; a `set_in_playbook` field shows "Set in playbook". `SchemaForm` renders only active top-level fields and never drops the values of others. Until Task 12, `TemplateDialog` hosts `VariableEditor variant="template"`.

New strings (en / de): origins "From playbook" / "Aus Playbook", "Adjusted" / "Angepasst", "Manual" / "Manuell", "Gone from source" / "Entfällt"; "Active" / "Aktiv"; "Reset to source" / "Auf Quelle zurücksetzen"; "Source says: {value}" / "Quelle: {value}"; "Keep as manual field" / "Als manuelles Feld behalten"; "Set in playbook" / "Im Playbook gesetzt".

- [ ] **Step 1: Pure move commit:** `git mv`, fix imports and i18n keys, run `npx vitest run src/features/inventory src/features/templates src/features/instance src/features/variables`, `npx eslint src`, `npx tsc -b` → green. **Commit** `refactor(web): move the variable schema editor into its own feature`.
- [ ] **Step 2: Failing tests:**
  - `origin.test.ts`: label change on a `source` field → `adjusted`, `source_field` = old content; active-only change keeps `source`; an `adjusted` field stays adjusted with its `source_field`; `resetToSource` restores content and keeps `active: false`; `keepAsManual` → origin manual, no `source`; `isActive({})` true.
  - `VariableEditor.test.tsx`: one row per top-level field with the six columns; toggling Active emits `active: false`; "+ Field" → dialog → a manual field appended; expanding a repeater shows its sub-fields inline, editing one on a `source` field emits `origin: 'adjusted'`; "Add sub-field" disabled on a `source` field; template variant shows the four origin pills; group variant shows Scope in the dialog and the assignment preview; shared variant offers no Secret; the migrated builder tests (names, types, enum options, list item type, server problems at `fields[0].name`) pass.
  - `FieldDialog.test.tsx`: `source` name input read-only; adjusted default shows "Source says: 80" and "Reset to source" restores 80; gone shows "Remove" and "Keep as manual field".
  - `SchemaForm.test.tsx`: an inactive field is not rendered and its value survives an edit of another field.
- [ ] **Step 3:** run, expect failures. **Step 4:** implement; every file under ~400 lines. **Step 5:** vitest (variables, inventory, instance/shared, templates, `src/i18n/catalogue.test.ts`), eslint, tsc pass.
- [ ] **Step 6: Commit** `feat(web): full-width variable editor with a field dialog and inline sub-fields`.

### Task 11: SPA: "Used in"

**Files:** Modify `web/src/features/variables/usage.ts` (+ `usage.test.ts`), `FieldDialog.tsx`, `FieldDialog.test.tsx`, `web/src/i18n/{en,de}/variables.ts`.

**Interfaces:** `export function usageByField(schedules: readonly { id: string; name: string; extra_vars?: Record<string, unknown> | null }[], triggers: readonly { id: string; name: string; mapping?: { extra_vars?: Record<string, string> | null } | null }[]): FieldUsage` (callers pass the template's own schedules and `template_run` triggers; Task 12 wires `useSchedules(org, templateId)` and `useTriggers(org, { kind: 'template_run', id })`).

- [ ] **Step 1: Failing tests:** schedule `nightly` overriding `{region, token}` and webhook `deploy-hook` mapping `{region}` → `region` = `[schedule nightly, webhook deploy-hook]`, `token` = `[schedule nightly]`, `port` absent; `FieldDialog` with that usage lists both under "Used in" / "Verwendet in" with links (`/o/$org/schedules?id=<id>`, the template's triggers dialog); no usage → "Not overridden anywhere" / "Nirgends überschrieben".
- [ ] **Step 2:** run, expect failure. **Step 3:** implement. **Step 4:** vitest, eslint, tsc pass.
- [ ] **Step 5: Commit** `feat(web): show where a template variable is overridden`.

### Task 12: SPA: the template variables page

**Files:** Create `web/src/features/templates/variables/TemplateVariablesPage.tsx`, `SourcePanel.tsx`, `SyncPreviewDialog.tsx`, `ReviewBadges.tsx`, `web/src/features/templates/PlaybookPicker.tsx`, their tests, `web/src/api/hooks/useTemplateVariables.ts`. Modify `web/src/api/hooks/useRepos.ts` (`useRepoPlaybooks(org, repoId)`, `usePickRepoPlaybook(org, repoId)`), `web/src/api/hooks/useTemplates.ts` (update sends `If-Match`), `web/src/router.tsx` (route `/templates/$id/variables` under `orgRoute`), `web/src/features/templates/TemplateDialog.tsx` (no schema/values section; `PlaybookPicker`; "Variables" link in edit mode; PUT echoes the loaded `extra_vars_schema` and keep-marker values with `If-Match`), `TemplatesPage.tsx` (review badges, "Variables" action), `vars.ts` (`varsForPut` uses active fields), `TemplatesPage.test.tsx` (dialog tests of schema/values move to the page test), `web/src/i18n/{en,de}/templates.ts`.

**Interfaces:** hooks `useTemplateVariables(org, id)`, `useUpdateTemplateVariables(org, id)`, `useSetVariablesMode(org, id)`, `usePreviewTemplateVariables(org, id)`, `useApplyTemplateVariables(org, id)` over Task 8/9's operations; each write takes `revision` and sends `If-Match: "<revision>"`.

Behaviour and copy (en / de):
- Badges: "{n} fields gone from the source — review" / "{n} Felder entfallen in der Quelle — prüfen"; "{n} changes need review" / "{n} Änderungen brauchen Prüfung"; "{n} required fields have no value" / "{n} Pflichtfelder ohne Wert".
- `SourcePanel`: playbook path or "Inline playbook" / "Inline-Playbook"; commit (first 7 chars) and sync time; "Newer commit available" / "Neuerer Commit verfügbar"; mode Off / Manual / Automatic (Aus / Manuell / Automatisch); "Sync now" / "Jetzt abgleichen" (preview); a link to the repository's sync for `repos.write`; notices: "The worker that synced this repository does not read variable sources." / "Der Worker, der dieses Repository abgeglichen hat, liest keine Variablenquellen."; incomplete record; "Roles are not read for an inline playbook." / "Bei einem Inline-Playbook werden Rollen nicht gelesen."; a projection is read-only.
- Choosing Manual or Automatic on an unlinked template opens the preview first and links through apply (no mode PUT); Manual ↔ Automatic and → Off use the mode PUT (Off asks for confirmation).
- `SyncPreviewDialog`: Added (with "active"/"inactive" chips), Changed (old → new per attribute, a review badge, and an input to re-enter the value for reason `value` or `secret`), Adjusted (the new source version), Gone with "Also remove gone fields" / "Entfallene Felder auch entfernen" (off by default), "{n} unchanged" / "{n} unverändert", notes with file:line; 409 → "The source changed — preview again" / "Die Quelle hat sich geändert — bitte erneut abgleichen"; 412 → the stale notice with reload.
- The page body: `VariableEditor variant="template"` with `usage` from Task 11, the values `SchemaForm` (active fields), Save (PUT variables, If-Match; 412 → reload notice).
- `PlaybookPicker`: "Inline playbooks" or a repository; for a repository its discovered paths; an unregistered path is picked (`usePickRepoPlaybook`) before the template is created/updated with the returned id; a repository without a record shows "Not synced yet — sync the repository to list its playbooks" / "Noch nicht abgeglichen — gleiche das Repository ab, um seine Playbooks zu sehen".

- [ ] **Step 1: Failing tests:**
  - `TemplateVariablesPage.test.tsx`: shows `9f8e7d6` and the sync time; `newer: true` shows "Newer commit available"; `worker_reads: false` shows the notice; "Sync now" opens the preview with added/changed/gone; Apply posts `{mode: 'manual', source_rev, remove_gone: false}` with `If-Match: "3"`; ticking "Also remove gone fields" posts `remove_gone: true`; 409 shows "The source changed — preview again"; Save on 412 shows the reload notice; Automatic on an unlinked template opens the preview and sends no mode PUT; Manual → Automatic PUTs `{mode: 'automatic'}`; the values form omits inactive fields; badges show 1/1/1 for review `{gone: 1, needs_review: 1, missing_required: 1}`.
  - `PlaybookPicker.test.tsx`: lists a repo's discovered paths; an unregistered path calls the pick endpoint, then create carries the returned `playbook_id`; a registered path uses its id without a pick call; an unsynced repo shows the hint.
  - `TemplatesPage.test.tsx`: edit PUT carries `If-Match` and the loaded schema unchanged with keep markers; a linked template row shows its badges and a "Variables" link; the secret keep-marker and 422-at-field tests now live in the page test.
- [ ] **Step 2:** run, expect failures. **Step 3:** implement. **Step 4:** vitest (templates, variables, `router.test.tsx`, catalogue), eslint, tsc pass.
- [ ] **Step 5: Commit** `feat(web): a variables page for job templates with source sync and preview`.

### Task 13: End to end

**Files:** Create `internal/e2e/variables_test.go`, `internal/e2e/fixtures/git/vars_demo.yml`, `internal/e2e/fixtures/git/roles/greeter/meta/argument_specs.yml`, `internal/e2e/fixtures/git/roles/greeter/tasks/main.yml`, `web/e2e/variables.spec.ts`. Modify `internal/e2e/harness_test.go` (`materializeFixtureRepo` copies the fixture tree recursively; `gitServer` exposes its bare path; a helper `pushFixtureChange(t, bare string, files map[string]string)` commits a change into the bare repository).

Fixture: `vars_demo.yml` = play `hosts: all`, `vars: {greeting_name: World  # pulsix: required Greeting name}`, `roles: [greeter]`; `greeter` specs `greeter_color` (str, choices `[red, blue]`, default `red`) and `greeter_count` (int, default 1).

- [ ] **Step 1: Failing test** `TestVariableSourcesEndToEnd` (real manager, worker and git server via `wireScenario`, whose setup already syncs the fixture repo): `GET …/repos/{id}/playbooks` lists `vars_demo.yml` with 3 fields and a 40-char commit; pick it; create a template; preview → added `greeting_name` (active), `greeter_color`, `greeter_count` (inactive); apply `manual`; set `greeting_name`; run → succeeded; mode `automatic`; push a change (add `greeter_style` optional, `greeter_count` default 2, drop `greeter_color`); sync → template has `greeter_style` inactive, `greeter_count` default 2, `greeter_color` with origin `gone` still present; revision bumped; `GET …/variables` review `gone: 1`; the next run still succeeds.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/e2e/ -run TestVariableSourcesEndToEnd`, expect failure until the fixture and helpers exist; then pass. The other e2e tests still pass (the fixture repo gained files).
- [ ] **Step 3: Playwright** `web/e2e/variables.spec.ts` (no worker needed): an editor playbook with `vars_prompt: [{name: admin_user, prompt: "Admin user", private: false}]` and `http_port: 8080  # pulsix: required HTTP port` → a template → "Variables" → Manual → the preview lists 2 added → Apply → both rows show "From playbook" → switch `admin_user` inactive → the values form shows only "HTTP port" → change its label in the dialog → "Adjusted" → "Reset to source" → "From playbook"; a group's Schema tab adds a manual field through "+ Field".
- [ ] **Step 4:** `npx playwright test e2e/variables.spec.ts` passes against the compose stack.
- [ ] **Step 5: Commit** `test(e2e): variables from a playbook source, end to end`.

---

## Controller follow-ups (not tasks of this plan)

- noix-docs (branch `pulsix`): the new reference page for inline markers and the sidecar (the EBNF of Task 2, type inference, every keyword, the argument-spec mapping table, precedence, limits, examples, and every note code of Tasks 2, 3 and 7 — `source_unavailable`, `not_checked`, `manual_name_taken`, `read_failed`, `report_too_large`, `too_many_playbooks`, `path_unsupported` included — with its cause); updates to "Inventory and variables" (active switch, the new editor), "Playbooks and jobs" (repos discover their playbooks, the template variables page, modes, preview, review badges), "Shared resources" (manual fields only). No tech-stack names.
- Backlog: close 7.75; file the galaxy roles path defect (`<repo>.galaxy` not on the run's roles path, `internal/worker/runner/layout.go`).
- Decisions 4–7 above are interpretations; confirm with the owner when writing the reference page.
