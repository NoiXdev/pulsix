# Agent Secrets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The agent reads named secrets from a protected local `agent.yaml`, reports only their names and the file's state to the manager, and hands a secret's fields to a plugin whose `secret` parameter names it; the manager stores and shows names/state and warns about missing secrets.

**Architecture:** New agent package `internal/agent/secrets` (load, protect, watch). The agent loop wires it; the scheduler resolves a check's `secret` parameter for plugins implementing an optional `checks.SecretPlugin` interface. Heartbeat carries names and state (additive proto fields) into `host_agents` (additive migration). `internal/checkplugins` gains the `secretName` format. Host API + SPA show names/state and a missing-secret warning.

**Tech Stack:** Go (gopkg.in/yaml.v3, protobuf via buf), Postgres migration, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-12-agent-secrets-design.md`

## Global Constraints

- File `agent.yaml` in the agent data dir (`/etc/pulsix`, `/Library/Application Support/Pulsix`, `%ProgramData%\Pulsix`); `--config` overrides; optional.
- Format: top-level `secrets:` only (unknown top-level keys refused); secret name `^[A-Za-z0-9_.-]{1,64}$`; each secret a map of string fields; ≤ 1 MiB file, ≤ 256 secrets, each value ≤ 4 KiB.
- Protection: exactly the agent's existing env-file rule (`internal/agent/protect*.go` `CheckEnvFile` — reuse, don't copy); a failing file is not read.
- Reload (changed during implementation): at start and then a full re-read on every look, at most once a minute, whether or not mtime or size changed. A broken file → no secrets + error state (never the last good set).
- Never send, log or include in any error a field value. Parser errors reduced to line/column.
- Heartbeat: `repeated string secret_names`, `string secrets_state` (`absent` | `ok` | error text). Old agents send neither → manager shows "unknown (agent too old)".
- Migration additive: `host_agents.secret_names text[]`, `host_agents.secrets_state text`.
- Missing secret at collection → Err on the plugin's host-level item: "secret `<name>` is not defined in agent.yaml".
- Plugins get fields only through `checks.SecretPlugin` (optional interface: `CollectWithSecret(ctx, params, fields map[string]string) ([]Observation, error)`), resolved by the scheduler.
- Repo rules: TDD; never `go test ./...` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …`); `golangci-lint run` and `GOOS=windows golangci-lint run ./internal/agent/...`; `GOOS=linux|darwin|windows go vet ./internal/agent/...`; buf generate for proto; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never touch the unrelated Docker containers here; public docs name no tech stack.

## Review Focus

1. A secret value reaching a log line, an error string, a heartbeat, a check result, or a panic message (YAML parse errors that quote the line!).
2. A file that is world-readable for a moment while being edited (editor writes a temp file and renames) — the watcher must re-check protection on every read.
3. A secret name in a check's params that is not in the file, or a file that disappears — the check reports the error every round and recovers when fixed.
4. Windows: DACL check on the file reuses the existing code; a file created by an editor running as an admin user.
5. An old agent (no new heartbeat fields) and a new agent side by side in the GUI.

---

### Task 1: `internal/agent/secrets` — load, protect, watch

Package with: `Load(path string, p protector) (Set, error)` (Set = names → fields; error states as values), a `Watcher` that re-reads in full at most once a minute (changed during implementation: no mtime/size shortcut; injectable clock), default path per OS (reuse the agent's data-dir helper), the `--config` flag wiring in cmd/agent. Tests: format, limits, unknown keys, bad names, protection refusal (reuse fakes from protect tests), parse errors without values, reload semantics, broken file drops all.

### Task 2: Heartbeat fields and scheduler resolution

Proto `Heartbeat.secret_names`, `secrets_state` (buf generate); agent loop sends them from the Watcher; `checks.SecretPlugin` + scheduler resolution (+ the missing-secret Err); tests with a fake secret plugin; no plugin uses it yet.

### Task 3: Manager stores and serves names/state; `secretName` format

Migration; agentapi heartbeat handling writes them with last_seen (old agents: leave NULL → "unknown"); agents store/service; host API agent DTO fields; `internal/checkplugins` format `secretName` (+ formats.json vectors, TS twin later in Task 4) and a helper that tells which descriptors have a `secret` param (none yet; test with a test-only descriptor); a host-level "missing secrets" computation for the host page (check params `secret` vs agent's names). schema.d.ts regenerated.

### Task 4: SPA

Host page agent section: secret names and file state (absent/ok/error/unknown); check rows referencing a secret the agent does not report show a warning; TS twin of `secretName` + formats vectors pass; i18n en/de; Vitest.

### Task 5: Documentation

`docs/BACKLOG.md` (3.5 done for secrets, 3.6 secret half done, new 3.5a static local checks), `docs/upgrades.md`; noix-docs (scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-agent-secrets` from `docs/pulsix-hardware-checks`): new page "Agent configuration file" (location per platform, format, permission commands per platform, what is reported, referring to a secret, plain-text-like-comparable-agents note). `npm run build`; push nothing.
