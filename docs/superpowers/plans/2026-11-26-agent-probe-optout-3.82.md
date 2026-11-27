# Agent-side probe opt-out and deny list (backlog 3.82) — implementation plan

> **For agentic workers:** steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 3.82: "Agent-side probe opt-out and deny list — Add an `agent.yaml` switch that stops the agent reporting `probes_supported` (so it is never assigned), plus agent-local deny CIDRs its guard refuses."

**Architecture:** `internal/agent/secrets` parses a new optional top-level `probes:` section (`enabled`, `deny_cidrs`) into a `ProbePolicy` on the `Set`. `internal/probe.Guard` gains a second deny list, the agent's own (`WithLocalDeny`), whose refusal is the fixed error `ErrLocalPolicy` (wrapping `ErrNotAllowed`); the probes report it through `probe.RefusalText`. `internal/agent/probes.Runner` gains `SetPolicy(enabled, deny)` (the deny list added to its default guard): disabled drops every slot, forgets the applied version and refuses `Apply`. The loop applies the policy on every look at the file (startup, heartbeat, periodic look) and the heartbeat reports `probes_supported = runner.Enabled()`. The manager already stops assigning an agent that reports false (3.13a); the agent DTO gains `probes_supported` and the host's Agent card shows it.

**Spec:** the backlog row above; 3.13a's design (`docs/superpowers/specs/2026-11-16-probe-vantages-design.md`) for everything that does not change.

## Global Constraints
- Layers per `CLAUDE.md`; package tests only (`go test -race -timeout 900s ./internal/<pkg>/...`, `internal/api` with `-timeout 1800s -run …`); never `go test ./...`, `make test` or `internal/e2e`.
- `golangci-lint run` on touched packages and `gofmt -s -l` clean before each commit; SPA `npx vitest run <files>`, `npm run lint`, `npx tsc -b`.
- `web/src/api/schema.d.ts` only through `npm run api:gen`.
- No migration, no proto change (`Heartbeat.probes_supported` and `host_agents.probes_supported` exist since 3.13a).
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **R1 Syntax: a top-level `probes:` mapping** with `enabled: true|false` (default true) and `deny_cidrs: [<CIDR>, …]` (default none). Unknown keys, duplicates, a non-boolean `enabled` (a quoted `"false"` included), a null `enabled`, a list entry that is not a single CIDR, more than 256 entries refuse the whole file like every other `agent.yaml` mistake. — The file's existing shape (sections by name, strict parsing); "enabled" reads plainly; `deny_cidrs` mirrors `PULSIX_PROBE_DENY_CIDRS`. — Cost if wrong: a rename later needs an alias.
- **R2 CIDRs only, as on the manager**: `netip.ParsePrefix`, so a bare address (`10.0.0.5`) is refused with "write 10.0.0.5/32 for a single address"-style guidance; host bits are masked and an IPv4-mapped prefix of /96 or longer is judged as IPv4 (the guard canonicalises, as for `PULSIX_PROBE_DENY_CIDRS`). Errors name line and column and a fixed reason, never the entry (the file's convention). — Cost if wrong: an operator writes `/32`.
- **R3 A refused file turns probing off (fail closed).** While `agent.yaml` is refused (syntax, protection, any mistake) the agent reports `probes_supported = false` and drops its slots; an absent file means the defaults (probing on, no deny list). — The file may hold `enabled: false` or a deny list the operator relies on; a typo elsewhere must not silently turn probing for others back on (the file's rule: a failure leaves none of the file's settings, never the last good ones). — Cost if wrong: an agent with a broken `agent.yaml` leaves its vantage groups until the file is fixed; the Agent card already shows the refusal.
- **R4 Opting out drops the running set and forgets its version.** `SetPolicy(false, …)` clears every slot (rounds in flight finish, their results are discarded — the runner's existing rule for a removed slot) and resets the applied version to "". — If the version were kept, re-enabling would report the same version the manager still holds and the manager would never send the set again. — Cost if wrong: none known.
- **R5 An assignment arriving while opted out is refused locally**: `Runner.Apply` returns false and changes nothing; the loop logs a warning. The manager sends none to an agent that reports false (3.13a), so this is only the race of a heartbeat sent before the file changed.
- **R6 A local refusal has its own fixed text**: `target address not allowed by the agent's local policy` (`probe.ErrLocalPolicy`, which `errors.Is` `ErrNotAllowed`, so every existing check of the refusal still holds). Nothing names the list or the matched range. An address refused by both the built-in policy and the local list reads as the built-in refusal (`target address not allowed`). Where one probe sees several refusals (an http redirect chain, a name resolving to several addresses), any local refusal makes the text the local one: tcp, tls and http dial through `Guard.DialerNoting`, which notes every address the local list refuses (net.Dialer reports only one address's error), and a failed dial with such an address reads as the local refusal even when another address refused the connection (fix round 1, L2); icmp gets the same from `ResolveAllowed`; a dns server is a single literal. — The vantage table shows the agent's error per vantage, and an operator must be able to tell "this agent refuses" from "nobody may probe this". — Cost if wrong: one string.
- **R7 A refused round counts as no result, not as down.** The refusal is an error on `<plugin>.up`, which the combining step already reads as "no result" (it neither counts toward n nor toward down; with no other vantage the check reads "no vantage point has a recent result"). No change to combining or alerting. — An agent's local policy says nothing about the target's health; counting it as down would let one host's owner fail another's check. — Cost if wrong: an opted-in vantage that refuses a target keeps one of its group's `agents_per_group` places (R8).
- **R8 The manager is not told the deny list**, so it keeps assigning such an agent targets it will refuse; the agent cannot know in advance (names resolve at dial time). Documented: deny lists are a safety net, `enabled: false` or leaving the host out of vantage groups is how to opt out of a check. — Nothing needs the list on the manager, and it is not sent. It is not secret either: the distinct refusal text (R6) shows which targets the agent refuses, so anyone who can assign checks to the agent (a check with a free-form http URL in a vantage group holding it) can read the list back by probing addresses (review L1, accepted: the readers are members of the same organization, and what the list prevents — reaching those ranges — still holds). The user docs say so. A reassignment-on-refusal loop is a follow-up if it bites.
- **R9 Deny lists see through IPv4-in-IPv6 forms** (NAT64 well-known prefix, 6to4, Teredo, …, the forms the built-in policy already sees through): an address whose embedded IPv4 falls in a deny range is refused. This applies to the manager's `PULSIX_PROBE_DENY_CIDRS` too (the same guard function). — Otherwise `64:ff9b::a00:1` reaches 10.0.0.1 through a NAT64 gateway past a `10.0.0.0/8` deny. — Cost if wrong: a manager operator who denies a range but probes it through NAT64 on purpose (implausible) loses those probes. The agent has no setting for its own NAT64 prefixes (`PULSIX_NOTIFY_NAT64_PREFIXES`); left out.
- **R10 The policy follows the file within about a minute** (the periodic look, 30 s, with the watcher's one-minute re-read): no restart. A change of `enabled` or of the list is logged once.
- **R11 The Agent card shows "Probes other hosts: Yes / No"** from the existing `host_agents.probes_supported` (new `probes_supported` on the agent DTO). "No" covers an agent older than 3.13a, one opted out and one whose `agent.yaml` is refused (R3; the text names all three); telling them apart would need a proto field and a column for little gain (the version badge and the file's state sit next to it).
- **R12 Version skew**: an agent older than this release does not know `probes:` and refuses the whole `agent.yaml` (secrets, commands and local checks with it), and one of 3.13a or later keeps probing without the opt-out or the deny list (it predates R3) — update the agent first, as for `commands:` and `checks:`. Under an older manager the new agent's opt-out works (the flag exists since 3.13a); an older manager serves its own embedded pages without the row and returns no `probes_supported`, so only a new card answered by an older instance during a mixed rollout reads "No" for every agent.
- **R13 The deny list is added to the runner's default guard** (`Runner.SetPolicy(enabled, deny)` builds `Deps.Guard.WithLocalDeny(deny)`, once per list), never a guard built apart (fix round 1, L5): a later setting on the default cannot be lost on an agent with a deny list.

### Fix round 1 (review `3.82-review.md`, controller rulings)
- L2 → R6 amended (`Guard.DialerNoting`, `TestTCPLocalDenyAmongSeveralAddresses`, `TestTLSLocalDenyAmongSeveralAddresses`, `TestHTTPLocalDeny/one of several addresses`).
- L5 → R13 (`TestRunnerDerivesTheGuardFromItsDefault`).
- L1 → R8's reasoning corrected; no code change.
- L3 → user docs: the host's own resolver and DNS answer contents are outside the deny list (it covers addresses a probe connects to).
- L4, I2, I3 → `docs/upgrades.md`, R11, R12 and the card text ("turned off or refused in agent.yaml").
- I1 → Teredo, ISATAP and native IPv6 rows in `TestGuardDenySeesThroughEmbeddedIPv4`.

## Review Focus
- A typo cannot turn probing back on: `TestLoopRefusedFileTurnsProbingOff` (agent).
- An opted-out agent probes nothing and says so: `TestLoopOptOutStopsProbingAndReportsIt`, `TestRunnerDisabledRefusesApply`, `TestRunnerReenableForgetsVersion` (agent/probes).
- Every dial path honours the deny list: `TestGuardLocalDeny` (literal, mapped, NAT64-embedded), `TestResolveAllowedLocalDeny` (a name resolving to a denied address), `TestHTTPLocalDeny` (redirect, name), `TestTCPLocalDeny`, `TestTLSLocalDeny`, `TestICMPLocalDeny`, `TestDNSLocalDenyServer`, `TestDNSDialLocalRefusalIsAnError`; `TestRunnerAppliesTheLocalDeny` (agent/probes, through the runner); `TestLoopDenyListReachesTheProbes` (agent).
- The refusal never names the list: every assertion compares the fixed text.
- Parsing: `TestParseProbes` (accepts) and `TestParseRefusesProbes` (table, no entry quoted).

## Task 1: Guard: local deny list, fixed refusal text, embedded forms (`internal/probe`, `internal/outbound`)
- `outbound.Policy.EmbeddedIPv4(a)`; `probe.ErrLocalPolicy`, `(*Guard).WithLocalDeny(prefixes)`, `(*Guard).Check(ip) error`, `probe.RefusalText(err)`, `probe.Refusal` (concurrency-safe recorder for http/dns); deny lists judged on the embedded IPv4 too; `ResolveAllowed` returns `ErrLocalPolicy` when a candidate was refused only locally.
- Probes: tcp, tls, http, icmp, dns report `RefusalText`.
- Tests: `TestGuardLocalDeny`, `TestGuardDenySeesThroughEmbeddedIPv4`, `TestGuardDialerReportsTheLocalRefusal`, `TestResolveAllowedLocalDeny`, `TestRefusalText`, `TestRefusalRecorder`, `TestEmbeddedIPv4` (outbound), per-probe local-deny tests.
- [ ] Commit `feat(probe): a local deny list with its own refusal text`.

## Task 2: `probes:` in `agent.yaml` (`internal/agent/secrets`)
- New `probes.go` (`ProbePolicy{Disabled bool; DenyCIDRs []netip.Prefix}`, `parseProbes`), `parse.go` (top-level key), `set.go` (`Set.Probes()`, String/LogValue).
- Tests: `TestParseProbes`, `TestParseRefusesProbes`.
- [ ] Commit `feat(agent): probes section in agent.yaml`.

## Task 3: Runner policy and loop wiring (`internal/agent/probes`, `internal/agent`)
- `Runner.SetPolicy(enabled bool, deny []netip.Prefix)`, `Runner.Enabled()`, `Apply` returns false while disabled; the guard is read per dispatch.
- Loop: `applyProbePolicy(set, state)` in `lookAndApplyLocal`; heartbeat `ProbesSupported: l.probes.Enabled()`; `applyProbeConfig` logs a refused push.
- Tests: `TestRunnerDisabledRefusesApply`, `TestRunnerReenableForgetsVersion`, `TestRunnerDisabledDiscardsTheInFlightRound`, `TestRunnerDerivesTheGuardFromItsDefault`, `TestRunnerAppliesTheLocalDeny`; `TestLoopOptOutStopsProbingAndReportsIt`, `TestLoopRefusedFileTurnsProbingOff`, `TestProbePolicyOf`, `TestLoopDenyListReachesTheProbes`.
- [ ] Commit `feat(agent): agent.yaml turns probing for other hosts off or narrows it`.

## Task 4: Agent card (`internal/agents`, `internal/api`, SPA)
- `Agent.ProbesSupported` from the row; `agentDTO.ProbesSupported`; `schema.d.ts` regenerated; `HostAgentCard` row, i18n en/de.
- Tests: `TestAgentDTOReportsProbesSupported` (api), `TestProbesSupportedAndOnlineCapableAgents` (agents, read path), `HostAgentCard.test.tsx` ("probes for other hosts").
- [ ] Commit `feat(web): the agent card shows whether the agent probes other hosts`.

## Task 5: Docs
- Backlog 3.82 → done; `docs/upgrades.md` (R12, R9).
- [ ] Commit `docs: backlog 3.82 done`.
