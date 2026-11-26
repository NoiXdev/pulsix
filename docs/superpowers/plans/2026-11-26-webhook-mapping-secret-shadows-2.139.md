# Webhook mapping secret shadows (backlog 2.139) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A webhook trigger's mapping can no longer target a variable name that is a secret anywhere the run takes values from, so an external sender cannot replace a secret's value.

**Architecture:** The run-request check `runrequests.(*Resolver).checkSecretShadows` (inventory secrets of the target hosts, playbook-declared secrets, vault-encrypted repository variables with the incomplete-read fallback) moves into a new small package `internal/secretshadow` that both `runrequests` and `triggers` use (`runrequests` imports `triggers`, so the check cannot stay in `runrequests`). Triggers run it on save (create/update of a `template_run` trigger with mapped extra vars) and on every delivery after the mapping is applied, before anything is enqueued or collected for a settle delay.

**Tech Stack:** Go, Postgres (no migration).

**Spec:** none beyond the backlog row 2.139 (docs/BACKLOG.md) and the run-request rule it extends: docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md (Part A, final review I2: "secrets are never set by the assistant"; backlog 2.130 and 2.134). Rulings below are the controller's.

## Global Constraints

- No migration. No change to the run-request behaviour or its error texts (existing `internal/runrequests` tests must pass unchanged, except for moved unit tests).
- Error texts never echo a value; they name the variable and the kind of secret, as the run-request messages do today.
- Layers per CLAUDE.md: `internal/secretshadow` declares its own small interfaces, no SQL, no HTTP types.
- Never run module-wide `go test ./...`; use `make test` / `scripts/test-groups.sh` with timeouts (`internal/api` 1800 s, `internal/e2e` on its own, others 900 s).
- Commits: Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings

- R1 Hosts for the inventory part: on save and at delivery, every host the template's scope reaches **before any limit** (`jobs.Service.RunTargets`). A mapped limit can only narrow the scope, so this is a superset: a name that is a secret for any reachable host is refused. Cost if wrong: a mapping onto a name that is a secret only for hosts the sender could never target is refused; the operator renames the variable.
- R2 At delivery a shadowing target fails the delivery as a payload problem in the same way other mapping refusals do (`mapPayload` returns field `extra_vars.<name>` and the error; outcome and reason recorded as for any other rejected payload). No run starts, nothing is collected into a pending settle run.
- R3 At save the refusal is `ErrInvalidInput` (HTTP 400/422 as today for mapping errors) with the same wording as the run-request messages, adapted to "mapping target" instead of "extra var".
- R4 A delivery also re-checks because secrets can appear after the trigger was saved (a new inventory secret, a playbook change, a sync that found a vault value).
- R5 The check needs only the mapping's **target names**, not values; with no mapped extra vars it does nothing (no reads).
- R6 A `template_run` trigger whose template has no playbook (`PlaybookID == uuid.Nil`) only gets the inventory part, matching runrequests.

## Review Focus

- A trigger saved before this change with a shadowing mapping: the next delivery is refused (R4) and the reason names the variable, not the value.
- Vault names unknown (older worker, incomplete read): a mapping onto a playbook-declared unreadable name is refused, any other passes (the 2.134 fallback), both on save and at delivery.
- A settle-delay trigger (2.137): the refused delivery must not reset or extend the quiet window or add to the pending run's collected values.
- Test deliveries ("Send test", `Service.Test`) go through the same path and are refused the same way.
- Wiring: both `internal/app/app.go` and `internal/api/apitest/apitest.go` pass the new dependency; a nil dependency must refuse a mapping with extra vars (fail closed, like `errNoSecretSources`), not skip the check.

---

### Task 1: Extract the check into `internal/secretshadow`

**Files:**
- Create: `internal/secretshadow/check.go`, `internal/secretshadow/check_test.go`
- Modify: `internal/runrequests/resolve_secrets.go` (delegate), its tests if they test `checkVaultNames` directly (move them)

**Interfaces:**
- Produces:
  ```go
  package secretshadow
  type InventorySecrets interface { SecretVarNames(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error) }
  type PlaybookVars interface { VarSource(ctx context.Context, orgID, playbookID uuid.UUID) (playbooks.VarSource, error) }
  type Checker struct { Inventory InventorySecrets; Playbooks PlaybookVars }
  var ErrNoSources = errors.New("secret shadow check: no secret sources")
  // ErrShadow is wrapped by every refusal; callers wrap it into their own ErrInvalidInput.
  var ErrShadow = errors.New("names a secret")
  // Check refuses the first of names (sorted) that is a secret the run's other sources hold.
  // what is the noun used in messages ("extra var", "mapping target").
  func (c Checker) Check(ctx context.Context, orgID, playbookID uuid.UUID, hosts []uuid.UUID, names []string, what string) error
  ```
- Messages keep today's wording with `what` in place of "extra var", e.g. `"%s %s: the inventory holds a secret of that name for a target host, which %s must not replace"` — keep the three existing sentences (inventory, playbook-declared, vault with file, unreadable fallback) exactly as they read now when `what == "extra var"` and the article form "an extra var".
- A `playbooks.ErrNotFound` from `VarSource` is returned wrapped unchanged so runrequests' `mapNotFound` keeps working.

- [ ] Step 1: Move `checkVaultNames` and its unit tests to the new package; write `Check` tests with fakes: inventory hit, playbook-declared secret hit (`schema.HoldsSecret`), vault name hit naming the file, unreadable fallback when `Vault` is nil or incomplete, editor playbook skips vault, `playbookID == uuid.Nil` skips playbook reads, empty names → no reads, nil sources → `ErrNoSources`.
- [ ] Step 2: Run them, see them fail; implement; run green (`go test -race -timeout 900s ./internal/secretshadow/`).
- [ ] Step 3: `Resolver.checkSecretShadows` keeps its signature and becomes a thin call: wraps `ErrNoSources` as `errNoSecretSources` behaviour today, and wraps refusals in `ErrInvalidInput` with identical text. Run `go test -race -timeout 900s ./internal/runrequests/` unchanged green.
- [ ] Step 4: Commit `refactor(runrequests): move the secret shadow check to its own package`.

### Task 2: Triggers refuse shadowing mapping targets on save and at delivery

**Files:**
- Modify: `internal/triggers/service.go` (Deps gets `Secrets SecretShadows` and `Scope ScopeTargets`, or one combined interface — implementer's choice, documented), `internal/triggers/validate.go` or a new `internal/triggers/secrets.go`, `internal/triggers/receive.go` (`mapPayload`), `internal/app/app.go`, `internal/api/apitest/apitest.go`
- Test: `internal/triggers/*_test.go` (unit with fakes), one API-level test in `internal/api` that a create/update with a shadowing mapping is refused with 4xx and the message, and one that a delivery to a trigger whose target became a secret later is refused (outcome recorded, no job enqueued).

**Interfaces:**
- Consumes: `secretshadow.Checker.Check` (what = `"mapping target"`), `jobs.Service.RunTargets(ctx, orgID, templateID) ([]uuid.UUID, error)` (R1).
- The template's `PlaybookID` comes from the `templates.Template` already loaded on save; at delivery load it through `Deps.Templates` (extend `TemplateChecker` with `Get` if it lacks it).

- [ ] Step 1: Failing unit tests: save refused for inventory/playbook/vault shadow (R3); save with no mapped extra vars does no secret reads (R5); delivery refused (R2, R4) with field `extra_vars.<name>` and a reason that does not contain the payload value; settle trigger's refused delivery leaves the pending row untouched; `Test` delivery refused alike; nil checker with mapped extra vars → refused (fail closed).
- [ ] Step 2: Implement; run `go test -race -timeout 900s ./internal/triggers/ ./internal/secretshadow/ ./internal/runrequests/`.
- [ ] Step 3: API tests; run `go test -race -timeout 1800s ./internal/api/ -run 'Trigger'`.
- [ ] Step 4: Commit `fix(triggers): a mapping never targets a secret's name`.

### Task 3: Docs

- [ ] Backlog row 2.139 → done with date 2026-11-26 and branch name; `docs/upgrades.md`: one line under the current release notes that a trigger whose mapping targets a secret's name now fails its deliveries (rename the target).
- [ ] User docs in the scratch clone `/Users/noidee/_dev/.worktrees/pulsix-session-lock/.superpowers/docs-clone` (branch `docs/pulsix-approval-loose-ends`): `webhook-triggers.md` mapping section gets the rule (no tech-stack names). Controller does this part, not the implementer.
- [ ] Commit `docs: backlog 2.139 done`.
