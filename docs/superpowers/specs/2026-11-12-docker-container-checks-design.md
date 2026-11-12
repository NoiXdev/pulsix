# Docker container checks (backlog 3.12) — design

Status: the owner asked on 2026-11-12 to build 3.12 and skipped the
questions ("gerne los"). The one thing they decided: **which containers are
watched is either all of them or only those marked with a label, and the
default is all.** Every other decision below is marked **(Claude's call)**.

## Intent

Many hosts Pulsix watches run their services as Docker containers. Today
the agent sees the host (cpu, memory, disks, systemd units) but not the
containers on it, so a crashed or unhealthy container goes unnoticed unless
a synthetic check happens to hit its port. 3.12 lets the agent report every
container's state and health, so a rule can raise an alert when a container
stops, keeps restarting or turns unhealthy.

## Goals

- A new agent-side check plugin, `docker`, configured and bound like every
  other plugin (org, monitoring group, host; same precedence, intervals,
  GUI). Its results land in the same current values and samples, so rules,
  alerts, graphs and the fleet view work on them unchanged.
- **Selection (owner's decision):** a check watches either every container
  on the host (default) or only the containers that carry a marker label.
- It works wherever the agent runs and a Docker Engine API is reachable:
  Linux, macOS (Docker Desktop) and Windows (Docker Desktop or Docker
  Engine on Windows Server).

## Non-goals

- Per-container resource usage (cpu, memory, network, block I/O). It needs
  the stats endpoint, which is slow per container, and its own rate
  computation. New backlog row **3.12a**. **(Claude's call.)**
- Starting, stopping or restarting containers. Pulsix reads, it never acts
  on the engine.
- Remote Docker hosts over TCP, and the `DOCKER_HOST` environment
  variable. The agent talks only to a local socket or named pipe.
  **(Claude's call:** no TLS client material to manage, and a remote engine
  is watched by installing the agent there.)
- Kubernetes, containerd without the Docker API, Swarm services.
- Seeding a `docker` check into organizations. Most hosts do not run
  Docker, and a seeded check would show a collection error on each of them.
  The operator adds it to the hosts or groups that need it. **(Claude's
  call.)**
- Forgetting a container that was removed on purpose. It behaves like an
  unmounted disk today: its last values stay and are badged stale. New
  backlog row **3.12b**. **(Claude's call.)**

## Parameters

| Parameter | Values | Default |
|---|---|---|
| `selection` | `all` or `labeled` | `all` |
| `label` | `key` or `key=value` | `pulsix.monitor=true` |
| `socket` | an absolute Unix socket path, or `npipe:////./pipe/<name>` | Linux and macOS `/var/run/docker.sock`; Windows `npipe:////./pipe/docker_engine` |

- `selection: all` watches every container the engine knows, running or
  not. A container whose labels carry the label's key with the value
  `false` (with the default, `pulsix.monitor=false`) is left out, so one
  container can opt out without switching the whole host to `labeled`.
  **(Claude's call.)**
- `selection: labeled` watches only containers that match `label`: with
  `key=value`, the label must have exactly that value; with a bare `key`,
  the label must be present with any value.
- Stopped containers are watched too: an exited container is exactly what
  this check exists to show.
- Validation on the manager, like every plugin:
  - unknown parameters are refused;
  - `selection` must be one of the two values;
  - the label key must be 1–255 bytes of letters, digits, `.`, `-`, `_` and
    `/`; the value must be at most 255 bytes with no control characters;
  - `socket` must be at most 255 bytes, contain no control characters and
    be either an absolute path beginning with `/` or a `npipe:////./pipe/`
    name.
  - `label` given with `selection: all` is accepted; it names the opt-out
    key.

## Metrics

Each watched container is one item, named by the container's name without
Docker's leading slash.

| Metric | Item | Kind | Meaning |
|---|---|---|---|
| `docker.state` | container | label | the engine's state: `created`, `running`, `paused`, `restarting`, `removing`, `exited`, `dead` |
| `docker.health` | container | label | `healthy`, `unhealthy`, `starting`, or `none` when the container has no health check |
| `docker.restart_count` | container | numeric | the engine's restart count for the container |
| `docker.exit_code` | container | numeric | the last exit code; reported only while the state is `exited` or `dead` |
| `docker.containers_running` | `""` | numeric | how many watched containers are running |
| `docker.containers_total` | `""` | numeric | how many containers are watched |

- When the engine cannot be reached (no socket, permission denied, engine
  stopped, a timeout), the two host-level metrics report a collection error
  that names the cause, for example "docker: cannot reach
  /var/run/docker.sock: permission denied". The per-container values stop
  arriving, turn stale, and rules on them go unknown after three intervals,
  as for every silent item.
- A container removed between listing and inspecting is skipped for that
  round, not reported as an error.
- An inspect that fails for another reason reports a collection error on
  that container's four metrics; the others are still reported.
- The rules catalogue gets all six metrics (check key `docker`), so rules
  such as `docker.state == "exited"`, `docker.health == "unhealthy"` or a
  windowed rise of `docker.restart_count` can be written.

## How the agent reads the engine

- The agent speaks the Docker Engine HTTP API over the socket or named
  pipe itself, with a small client: list the containers (all of them, with
  the label filter applied by the engine in `labeled` mode), then inspect
  each selected container for its health, restart count and exit code.
  **(Claude's call:** no Docker SDK dependency. The named pipe uses
  `github.com/Microsoft/go-winio`, already in the module graph.)
- It uses a fixed API version path that Docker 20.10 and later and
  Podman's Docker-compatible API all serve. **(Claude's call;** the builder
  picks the exact version and records why.)
- Only GET requests to the list and inspect endpoints. Never exec, logs,
  stats or anything that writes.
- Bounds: the whole collection runs within the check's own timeout; at
  most 8 inspects run at once; a response body is capped (the list at
  16 MiB, one inspect at 4 MiB) and a larger one is a collection error.
- The inspect response holds a container's environment, which often
  carries secrets. The client decodes only the fields it needs, and no
  error message ever quotes a response body. **(Claude's call.)**
- The agent runs as root (Linux, macOS) or as SYSTEM (Windows), which
  reaches the default socket or pipe without further setup.

## Older agents

An agent that predates 3.12 does not know the plugin and reports its usual
"unknown plugin" error for the check. `docs/upgrades.md` says a `docker`
check needs an agent at this version or later. No manager-side gating.
**(Claude's call.)**

## GUI

- The check dialog and the instance's shared check form offer `docker`
  with three fields: a selection choice (all containers / only labeled
  containers), the label (shown for both choices, with a hint that in "all"
  mode the value `false` opts a container out), and an optional socket with
  the platform defaults as placeholder. Client-side validation mirrors the
  server's.
- The host page shows each container as an item with its state and health,
  like the service check's units.

## Documentation

- `docs/BACKLOG.md`: 3.12 done; new rows 3.12a (resource usage) and 3.12b
  (forgetting a removed container).
- `docs/upgrades.md`: the new plugin and the agent version it needs.
- End-user docs (noix-docs, `reference/check-plugins.md`,
  `user/monitoring.md`): the plugin, its parameters, the label convention
  with a compose example, the Docker Desktop socket note for macOS, and
  example rules. As always, no tech stack named.

## Testing

- The plugin runs against a fake Engine API served on a Unix socket in a
  temporary directory (a named pipe in the Windows-only tests). Tests never
  touch a real Docker engine: the development machine runs about fifty
  unrelated containers.
- Server-side validation is table-tested; the SPA fields with Vitest.

## Left to the builder

- The package layout inside `internal/agent/checks` (suggested:
  `docker.go` for the plugin, `docker_client.go` for the HTTP client,
  `docker_dial_{unix,windows}.go` for the transport).
- The exact API version, the inspect concurrency mechanism and the error
  wording, within the bounds above.
