# Docker Container Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `docker` agent check plugin that reports every watched container's state, health, restart count and exit code, plus host-level counts, with "all containers" (default) or "only labeled" selection.

**Architecture:** The manager accepts and validates the new plugin and its three parameters and knows its six metrics in the rules catalogue. The agent gets a minimal Docker Engine API client (GET list and inspect over a Unix socket or Windows named pipe, no SDK) and a plugin that turns its answers into observations. The SPA offers the plugin with three fields through the existing parameter-field table.

**Tech Stack:** Go (agent and manager), `github.com/Microsoft/go-winio` for the named pipe (already in the module graph, becomes direct), React/TypeScript SPA with Vitest.

**Spec:** `docs/superpowers/specs/2026-11-12-docker-container-checks-design.md`

## Global Constraints

- Plugin name `docker`; check key default `docker`; rules catalogue check key `docker`.
- Parameters exactly `selection` (`all`|`labeled`, default `all`), `label` (`key` or `key=value`, default `pulsix.monitor=true`), `socket` (default Linux/macOS `/var/run/docker.sock`, Windows `npipe:////./pipe/docker_engine`). Unknown parameters refused.
- Label key: 1–255 bytes of `[A-Za-z0-9._/-]`; label value ≤ 255 bytes, no control characters. Socket ≤ 255 bytes, no control characters, either begins with `/` or with `npipe:////./pipe/` followed by a non-empty name.
- Metrics exactly: `docker.state` (label), `docker.health` (label), `docker.restart_count`, `docker.exit_code` (only while state is `exited` or `dead`), `docker.containers_running`, `docker.containers_total` (item `""`). Container item = name without the leading `/`.
- State labels: `created`, `running`, `paused`, `restarting`, `removing`, `exited`, `dead`. Health labels: `healthy`, `unhealthy`, `starting`, `none`.
- Only GET requests to the list and inspect endpoints. At most 8 inspects in flight. Body caps: list 16 MiB, inspect 4 MiB. No error text ever quotes a response body. Decode only the needed inspect fields (never `Config.Env`).
- No Docker SDK. No TCP engines, no `DOCKER_HOST`. No seeding of a `docker` check.
- Tests never touch a real Docker engine (the dev machine runs ~50 unrelated containers). Fake engines listen on a Unix socket under a short `os.MkdirTemp("", "dk")` directory (macOS caps socket paths at 104 bytes) or, on Windows, a uniquely named pipe.
- Repo rules: everything English; `make test` or single packages with `-race -timeout 900s` (1800s for `internal/api`), never `go test ./...`; `golangci-lint run` clean (also with `GOOS=windows`); commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; public docs name no tech stack.

## Review Focus

1. A container name with characters an item cannot hold, or two names that collide after the leading slash is cut — the item must still be the engine's name (no rewriting), and the plugin must not report the same item twice (Task 3 test `TestDockerReportsEachContainerOnce`).
2. The engine answers with an error status (500, 404 on the list, 400 on a bad filter) — a collection error naming the status, never the body (Task 2 test `TestClientNeverQuotesTheBody`).
3. A huge or never-ending response from something that is not Docker at the socket path — the cap and the context deadline stop it (Task 2 tests `TestClientCapsTheListBody`, `TestClientStopsAtTheDeadline`).
4. `selection: all` with a container labeled `pulsix.monitor=FALSE` or `pulsix.monitor=false ` — the opt-out compares the value case-insensitively after trimming (Task 3 test table rows).
5. An existing agent-side definition edited from `labeled` back to `all` with `label` still set — accepted, and the label then names the opt-out key (Task 1 validation row, Task 3 selection row).

---

### Task 1: Manager accepts the `docker` plugin

**Files:**
- Modify: `internal/checks/definitions.go` (constant, `supportedPlugins`, `validateParams` case, the plugin list in `ValidateDefinitionShape`'s error)
- Create: `internal/checks/docker_params.go`, `internal/checks/docker_params_test.go`
- Modify: `internal/rules/catalog.go` (six entries; extend the OBLIGATION comment's file list with `internal/agent/checks/docker.go`), `internal/rules/catalog_test.go` if it enumerates entries
- Modify: `internal/api/check_handlers.go:71,106,107`, `internal/api/host_check_handlers.go:143`, `internal/api/shared_handlers.go:404` (enum gains `docker`; the doc strings name it as run by the agent and describe its params)
- Test: an API test in `internal/api` that creates a `docker` check with `{"selection":"labeled","label":"pulsix.monitor=true"}` and gets 201, and one with `{"socket":"tcp://x"}` that gets the validation status the other plugins get.

**Interfaces:**
- Produces: `checks.PluginDocker = "docker"`; unexported `func validateDockerParams(params map[string]any) error` (wraps `ErrInvalidInput`).
- `IsSynthetic("docker")` stays false, so the resolver sends the check to the agent unchanged. `itemParam` gets no `docker` entry (items are not enumerable in params).
- `internal/shared` `reservedSeedKeys` unchanged.

- [ ] **Step 1: Write the failing table test** `TestValidateDockerParams` in `docker_params_test.go`, through `ValidateDefinitionShape("docker", "docker", params, time.Minute)`. Accepted: `{}`; `{"selection":"all"}`; `{"selection":"labeled"}`; `{"selection":"labeled","label":"pulsix.monitor"}`; `{"label":"com.example/watch=yes"}`; `{"selection":"all","label":"pulsix.monitor=true"}`; `{"socket":"/var/run/docker.sock"}`; `{"socket":"/run/podman/podman.sock"}`; `{"socket":"npipe:////./pipe/docker_engine"}`. Refused with `errors.Is(err, ErrInvalidInput)`: `{"selection":"some"}`; `{"selection":1}`; `{"label":""}`; `{"label":"=x"}`; `{"label":"bad key=x"}`; `{"label":"k=" + strings.Repeat("v", 256)}`; `{"label":"k=a\nb"}`; a 256-byte key; `{"socket":""}`; `{"socket":"docker.sock"}`; `{"socket":"tcp://10.0.0.1:2375"}`; `{"socket":"npipe:////./pipe/"}`; `{"socket":"/a\x00b"}`; a 256-byte socket; `{"units":["x"]}` (unknown parameter).
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/checks -run TestValidateDockerParams` — expect FAIL (unsupported plugin).
- [ ] **Step 3: Implement** the constant, `supportedPlugins` entry, the `validateParams` case calling `validateDockerParams`, and `docker_params.go`. A `label` splits at the first `=`; the key is checked against `^[A-Za-z0-9._/-]{1,255}$`, the value (possibly empty after `=`) against length and `unicode.IsControl`.
- [ ] **Step 4: Add the six catalogue entries** (`docker.state` and `docker.health` `MetricDiscrete`, the four others `MetricNumeric`, all `CheckKey: "docker"`) and a catalogue test row that `docker.state == "exited"` and `docker.restart_count > 3` resolve.
- [ ] **Step 5: Update the API enums and doc strings**, regenerate `web/src/api/schema.d.ts` with `web/scripts/api-gen.sh`, add the two API tests.
- [ ] **Step 6: Verify** `go test -race -timeout 900s ./internal/checks ./internal/rules ./internal/shared` and `go test -race -timeout 1800s ./internal/api -run 'Check'`, then `golangci-lint run`.
- [ ] **Step 7: Commit** `feat(checks): accept the docker plugin and its container metrics`.

### Task 2: Agent-side Docker Engine client

**Files:**
- Create: `internal/agent/checks/docker_client.go`, `docker_client_test.go`
- Create: `internal/agent/checks/docker_dial_unix.go` (`//go:build !windows`), `docker_dial_windows.go`, `docker_dial_windows_test.go`
- Modify: `go.mod` (`github.com/Microsoft/go-winio` moves to the direct block; `go mod tidy`)

**Interfaces:**
- Produces:
  ```go
  type containerSummary struct {
      ID     string
      Name   string            // first entry of Names, leading "/" cut
      State  string            // the list's State, lower-case
      Labels map[string]string
  }
  type containerDetail struct {
      Health       string // "healthy", "unhealthy", "starting", or "none" when State.Health is absent
      RestartCount int
      ExitCode     int
  }
  type dockerEngine interface {
      List(ctx context.Context, labelFilter string) ([]containerSummary, error) // labelFilter "" = no filter
      Inspect(ctx context.Context, id string) (containerDetail, error)
  }
  var errContainerGone = errors.New("container gone") // Inspect's answer to a 404
  func newDockerEngine(socket string) (dockerEngine, error) // socket "" = the platform default
  const defaultDockerSocket // per-OS, in the dial files
  ```
- The HTTP client uses a custom `DialContext` from the dial files (`dialDocker(ctx, socket string) (net.Conn, error)`), host `docker`, path prefix `/v1.41` (Docker 20.10+, Podman's compat API; record this in a comment). List: `GET /v1.41/containers/json?all=1[&filters={"label":["<filter>"]}]`. Inspect: `GET /v1.41/containers/<id>/json`.
- The dialer takes a plain absolute path or an `npipe:////./pipe/<name>` address, the two forms Task 1 accepts.

- [ ] **Step 1: Write the failing tests** in `docker_client_test.go` against a fake engine (`httptest.NewUnstartedServer` with a listener on `filepath.Join(os.MkdirTemp("", "dk"), "d.sock")`; skip on Windows):
  - `TestClientListsAllContainers`: the fake asserts method GET, path `/v1.41/containers/json`, `all=1`, no `filters`; returns two containers (`Names: ["/web"]`, `State: "running"`, labels) → two summaries with `Name == "web"`.
  - `TestClientSendsTheLabelFilter`: `List(ctx, "pulsix.monitor=true")` → the fake sees `filters` decoding to `{"label":["pulsix.monitor=true"]}`.
  - `TestClientInspects`: inspect JSON with `State.Health.Status "unhealthy"`, `RestartCount 4`, `State.ExitCode 137`, and a `Config.Env` holding `SECRET=x` → `containerDetail{Health:"unhealthy", RestartCount:4, ExitCode:137}`; no health block → `Health "none"`.
  - `TestClientInspectGone`: 404 → `errors.Is(err, errContainerGone)`.
  - `TestClientNeverQuotesTheBody`: 500 with body `TOPSECRET` on list and on inspect → error contains `500`, not `TOPSECRET`.
  - `TestClientCapsTheListBody`: a 17 MiB list body → error, and `TestClientCapsTheInspectBody`: 5 MiB → error.
  - `TestClientStopsAtTheDeadline`: a handler that never answers; `List` with a 200 ms context returns an error within 2 s.
  - `TestClientNoSocket`: `List` against a path that does not exist → error containing the path.
  - `docker_dial_windows_test.go`: `TestClientOverNamedPipe` serves the same fake on `winio.ListenPipe(`\\.\pipe\pulsix-test-<random>`, nil)` and lists through `npipe:////./pipe/pulsix-test-<random>`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/checks -run TestClient` — expect FAIL (undefined).
- [ ] **Step 3: Implement** the client and dial files. Errors read like `docker: list containers: status 500`; dial errors keep the OS cause (`docker: cannot reach /var/run/docker.sock: ...`). Bodies are read through `io.LimitReader(cap+1)` and refused when over the cap.
- [ ] **Step 4: Verify** the package tests, `GOOS=windows go vet ./internal/agent/checks`, `golangci-lint run` and `GOOS=windows golangci-lint run ./internal/agent/...`.
- [ ] **Step 5: Commit** `feat(agent): read containers from the Docker Engine API`.

### Task 3: The `docker` plugin

**Files:**
- Create: `internal/agent/checks/docker.go`, `docker_test.go`
- Modify: `internal/agent/checks/plugin.go` (`Registry()` gains `"docker": newDockerPlugin()`, package comment lists the plugin)
- Modify: any registry test that enumerates plugin names.

**Interfaces:**
- Consumes: Task 2's `dockerEngine`, `containerSummary`, `containerDetail`, `errContainerGone`, `newDockerEngine`; `valueObservation`, `labelObservation`, `errObservation`, `paramStrings`-style defensive parsing.
- Produces: `type dockerPlugin struct{ engine func(socket string) (dockerEngine, error) }`, `func newDockerPlugin() Plugin`, `Name() == "docker"`.

- [ ] **Step 1: Write the failing tests** in `docker_test.go` with a fake `dockerEngine` (records the label filter it got):
  - `TestDockerAllSelection`: default params; containers `web` running/healthy/restarts 0, `job` exited/none/exit 3/restarts 1, `cron` running with `pulsix.monitor=false` → items `web`, `job` only; `web`: `docker.state "running"`, `docker.health "healthy"`, `docker.restart_count 0`, no `docker.exit_code`; `job`: `docker.state "exited"`, `docker.exit_code 3`; `""`: `docker.containers_running 1`, `docker.containers_total 2`; the fake got filter `""`.
  - Table rows for the opt-out value: `false`, `FALSE`, ` false ` excluded; `0`, `no`, `` kept.
  - `TestDockerLabeledSelection`: `{"selection":"labeled"}` → fake got filter `pulsix.monitor=true`; `{"selection":"labeled","label":"watch"}` → filter `watch`; with `selection: labeled` no opt-out filtering happens client-side.
  - `TestDockerAllWithCustomLabelKey`: `{"label":"com.example/watch=yes"}` → opt-out key is `com.example/watch`.
  - `TestDockerSocketParam`: `{"socket":"/x.sock"}` → the engine factory got `/x.sock`; `{}` → got `""`.
  - `TestDockerEngineDown`: `List` fails → exactly two observations, both `Err` on item `""` for the two count metrics, and `Collect` returns a non-nil error.
  - `TestDockerContainerGoneMidway`: one inspect returns `errContainerGone` → that container is absent from items and from both counts; no error.
  - `TestDockerInspectFails`: one inspect returns another error → that container's `docker.state`, `docker.health`, `docker.restart_count`, `docker.exit_code` are `Err` observations; the others are values; counts still include it (its state came from the list).
  - `TestDockerReportsEachContainerOnce`: the list repeats an ID → one set of observations.
  - `TestDockerBoundsInspects`: 20 containers, a fake that tracks concurrent `Inspect` calls → peak ≤ 8.
  - `TestDockerDeadContext`: cancelled context → `nil, ctx.Err()`.
  - The registry test: `Registry()` has `docker`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/checks -run 'TestDocker|TestRegistry'` — expect FAIL.
- [ ] **Step 3: Implement** `docker.go`. `docker.exit_code` is emitted only for states `exited` and `dead`. Counts are over the containers that remain after selection and after dropping gone ones; `running` counts state `running`.
- [ ] **Step 4: Verify** the package, `golangci-lint run`, `GOOS=windows golangci-lint run ./internal/agent/...`.
- [ ] **Step 5: Commit** `feat(agent): report Docker containers' state and health`.

### Task 4: SPA offers the `docker` plugin

**Files:**
- Modify: `web/src/features/checks/CheckDialog.tsx:19` and `web/src/features/instance/shared/SharedCheckFormPage.tsx:60` (plugin lists gain `docker` after `uptime`)
- Modify: `web/src/features/checks/syntheticParams.ts` and `SyntheticParamFields.tsx`: the field table already drives synthetic plugins; add `docker` to it without making `isSynthetic('docker')` true. Add `export const FIELD_PLUGINS = [...SYNTHETIC_PLUGINS, 'docker'] as const`, `export function hasFieldTable(plugin: string): plugin is FieldPlugin`, and switch the dialogs' params/values/validation branches from `isSynthetic` to `hasFieldTable` where they build params (the "runs from the Pulsix server" marker keeps using `isSynthetic`).
- Docker fields: `selection` select `['all','labeled']` default `all`; `label` text, `check: 'dockerLabel'`, `trim: true`, placeholder `pulsix.monitor=true`, hint; `socket` text, `check: 'dockerSocket'`, `trim: true`, placeholder `/var/run/docker.sock`, hint naming the Windows pipe default.
- Modify: `web/src/i18n/en/checks.ts`, `web/src/i18n/de/checks.ts` (labels, hints, the two check messages, the select's option labels "All containers"/"Only labeled containers" and German equivalents).
- Test: `web/src/features/checks/CheckDialog.docker.test.tsx`, `syntheticParams.test.ts` rows.

**Interfaces:**
- Consumes: Task 1's server validation rules (mirror them exactly); `schema.d.ts` from Task 1.

- [ ] **Step 1: Write the failing tests:** choosing `docker` shows the three fields and no "runs from the Pulsix server" marker; saving untouched sends `params: {}`; choosing "Only labeled containers" and saving sends `{selection: 'labeled'}`; a label `bad key` and a socket `docker.sock` show field errors and send nothing; editing a stored `{selection:'labeled', label:'watch', socket:'/x.sock'}` shows those values and round-trips them; the shared check form offers `docker` and sends the same params. `syntheticParams.test.ts` rows for `dockerLabel` and `dockerSocket` mirroring Task 1's accepted/refused lists.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/checks src/features/instance/shared` — expect FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Verify** `npx vitest run`, `npm run lint`, `npm run typecheck` (or the repo's equivalents in `web/package.json`).
- [ ] **Step 5: Commit** `feat(web): offer the docker check with its selection, label and socket`.

### Task 5: Documentation

**Files:**
- Modify: `docs/BACKLOG.md` (3.12 → done with a one-paragraph evidence note; new planned rows 3.12a "Per-container resource usage (cpu, memory, network, block I/O) for the docker check" and 3.12b "Forget a container (or any item) that was removed on purpose instead of badging it stale forever")
- Modify: `docs/upgrades.md` (the `docker` plugin needs an agent at this version or later; an older agent reports "unknown plugin" for it; no manager-side gating; no seed)
- noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-docker-checks` from `docs/pulsix-windows-macos-agents`: `reference/check-plugins.md` (the plugin, parameters, metrics), `user/monitoring.md` (watching containers: label convention with a compose `labels:` example for opt-in and opt-out, the macOS Docker Desktop socket note, example rules `docker.state == "exited"` and `docker.health == "unhealthy"`), `reference/changelog.md` Unreleased line. No tech stack named beyond "Docker", which is the product being watched. Build the docs site as on the earlier branches.

- [ ] **Step 1: Edit the three repo docs and the noix-docs pages.**
- [ ] **Step 2: Verify** the docs build in the scratch clone passes.
- [ ] **Step 3: Commit** in pulsix `docs: record the docker check (backlog 3.12)` and in noix-docs `docs(pulsix): document the docker container check` (footer as above). Push nothing.
