# Agent secrets in a local configuration file (backlog 3.5 and the secret half of 3.6) — design

Status: the owner decided on 2026-11-12 to build local agent secrets before
the database checks (3.10), "wie Zabbix/Checkmk": credentials live in a
configuration file on the host, readable only by root/SYSTEM/administrators,
in plain text, and never leave the host. Every other decision below is
**(Claude's call)**. It follows the architecture spec
(`2026-09-10-pulsix-architecture-design.md`, "Local config", "agent secrets
never leave the host").

## Intent

Checks such as PostgreSQL, MySQL or Redis (3.10), web server status pages
behind authentication (3.11) and later HTTP checks with credentials need
secrets on the monitored host. The manager must never hold them. An operator
writes them once into a local file on the host, the way Zabbix agent 2's
plugin sessions or Checkmk's `mk_postgres.cfg`/`.my.cnf` work, and a check
defined in the GUI refers to one by name.

## The file

- Path: `agent.yaml` in the agent's data directory — `/etc/pulsix/agent.yaml`
  on Linux, `/Library/Application Support/Pulsix/agent.yaml` on macOS,
  `%ProgramData%\Pulsix\agent.yaml` on Windows. A `--config` flag overrides
  it. The file is optional; without it the agent has no secrets.
- Format (YAML):

  ```yaml
  secrets:
    pg_main:
      dsn: "postgres://monitor:s3cret@localhost:5432/postgres?sslmode=disable"
    mysql_shop:
      user: monitor
      password: "s3cret"
      host: 127.0.0.1
      port: "3306"
    redis_cache:
      password: "s3cret"
  ```

  A secret is a name (`[A-Za-z0-9_.-]`, 1–64 bytes) mapping to string
  fields. Which fields a check uses is that plugin's business (3.10
  documents them); the file format does not know plugins. Unknown top-level
  keys are refused, so a typo is loud. Static local checks (the
  architecture spec's `checks:` section) are not part of this item: new
  backlog row **3.5a**.
- Protection: the same rule the agent already applies to its `agent.env`
  (backlog 3.2/3.3 `CheckEnvFile`): owned by root (Unix) or by SYSTEM or the
  Administrators group (Windows), and not readable or writable by anyone
  else. A file that fails the rule is not read at all; the agent keeps
  running without secrets and reports why.
- Reload: the agent re-reads the file when its modification time or size
  changes, checked at most once a minute and at start. A broken file
  (unparsable, too open, a secret with a bad name) drops all secrets and
  reports the error, so a fix shows at once and nobody runs on a stale
  credential they believe they removed.
- Limits: the file at most 1 MiB, at most 256 secrets, each field value at
  most 4 KiB.

## What leaves the host

- **Never** a secret's field values.
- The agent reports, with every heartbeat: the **names** of the secrets it
  loaded, and the file's state (`absent`, `ok`, or an error message such as
  "agent.yaml is readable by other users" or "line 4: …"). An error message
  never quotes a value from the file (parser errors are reduced to line and
  column).
- The manager stores the latest names and state per agent and shows them on
  the host page's agent section.

## Checks that use a secret

- A plugin that needs credentials declares a parameter `secret` (a string
  in the descriptor, format `secretName`). 3.10's plugins are the first.
- The agent's scheduler resolves the name when it runs the check and hands
  the plugin the secret's fields alongside its parameters; the plugin never
  reads the file itself. A name the file does not define gives the check a
  collection error "secret `<name>` is not defined in agent.yaml" on its
  host-level item, every round, until it appears.
- The GUI warns where it can know: the host page shows a check whose
  `secret` names a secret the host's agent did not report, with the same
  message; the check form shows a hint that the secret lives in `agent.yaml`
  on each host. The manager does not refuse a definition whose secret no
  host has yet (definitions bind to groups and organisations).

## Wire and storage

- `Heartbeat` gains `repeated string secret_names` and `string
  secrets_state` (additive proto fields; an old agent sends neither, which
  the manager shows as "unknown — agent too old").
- `host_agents` gains `secret_names text[]` and `secrets_state text`
  (additive migration), written with `last_seen`.
- The host API's agent DTO and the host page show them.

## Platforms

Linux, macOS and Windows, using the existing per-OS protection code.

## Documentation

- `docs/BACKLOG.md`: 3.5 done (secrets part), 3.6 secret half done, new row
  3.5a (static local checks).
- `docs/upgrades.md`: the new file, the new heartbeat fields, the migration.
- End-user docs (noix-docs): a page "Agent configuration file" — location
  per platform, format, permissions (with the commands to set them on each
  platform), what is reported, how a check refers to a secret, and the
  security note that the file is plain text protected by file permissions,
  like comparable monitoring agents.

## Non-goals

- Encrypting the file, OS key stores, central secret management in the GUI.
- Static local checks (3.5a).
- Any plugin using secrets (3.10 is the first).

## Left to the builder

- The YAML library (the module already has `gopkg.in/yaml.v3`).
- How the scheduler passes the fields to the plugin (an extra argument, or a
  reserved key in the params copy the plugin receives) — it must not leak
  into results, logs or the advisory error.
