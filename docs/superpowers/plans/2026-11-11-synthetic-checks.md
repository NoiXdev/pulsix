# Synthetic checks (backlog 3.13) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The manager probes monitored hosts from outside with five new check plugins (`icmp`, `tcp`, `http`, `tls`, `dns`), configured and evaluated like every other check.

**Architecture:** Four parts.
1. A pure probe package `internal/probe` holds the five probes and a dial guard. It knows nothing of checks or the database.
2. `internal/checks` learns the plugins, validates their params, and splits each resolved set into the agent part and the synthetic part. The synthetic part is not pushed and not versioned.
3. A manager loop `internal/probes` runs the synthetic sets as the single advisory-lock holder and records results through `checks.Service.RecordObservations`.
4. The SPA gains the forms.

**Tech Stack:**
- Go standard library: `net`, `net/http`, `crypto/tls`, and `net.Resolver` with a custom `Dial` for `server`.
- `golang.org/x/net/icmp` for unprivileged ICMP. It is already in go.mod as an indirect dependency; make it direct at its current version.
- React SPA.

**Spec:** `docs/superpowers/specs/2026-11-11-synthetic-checks-design.md`. Read it first; this plan does not repeat its tables.

## Global Constraints

- Plugin names, exactly: `icmp`, `tcp`, `http`, `tls`, `dns`.
- Metrics, parameters, defaults and ranges are exactly the spec's "Plugins" table. The item is always `""`.
- A failed attempt reports `<plugin>.up = 0`, not an error. Only an attempt that could not be made reports an error observation: no address, a refused target, an unparsable target, or ICMP not permitted.
- Error texts, verbatim where named: `host has no address`, `target address not allowed`.
- The dial guard:
  - always refuses loopback, link-local, unspecified and multicast addresses;
  - refuses every CIDR in `PULSIX_PROBE_DENY_CIDRS` (comma-separated; whitespace around entries is trimmed; an invalid entry fails manager start-up with an error naming it);
  - is applied to the resolved IP of every dial: `net.Dialer.Control`, plus the custom DNS `server`;
  - in tests, the set of always-refused ranges is injectable.
- Response content never leaves the probe. Only the metrics and a collection error leave it.
- Probe runner bounds:
  - one advisory-lock holder across managers (`schedule.NewAdvisoryLocker`, new key `8314` named `AdvisoryKeyProbes` in `internal/probes`);
  - at most 32 concurrent probes;
  - a probe's timeout is `min(timeout_s, interval − 1s)`;
  - the resolved sets are refreshed at least every 30 s, so a definition or host change applies within one minute;
  - a new holder, after a restart or a failover, may probe each check once early; it never probes a check twice within one interval while it holds the lock.
- Synthetic checks are never in the set an agent receives or in its `Version`.
- No migration is needed. If one turns out to be, it is additive.
- Layering, testing and commands are per `CLAUDE.md`:
  - `go test -race -timeout 900s` on single packages, and `-timeout 1800s` for `internal/api`;
  - `golangci-lint run ./...` must be clean;
  - from `web/`: `npx tsc --noEmit -p .`, `npx eslint src`, `npx vitest run`;
  - API enum changes need `npm run api:gen` from `web/`.
- Conventional Commits. Footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push or tag. Never run `git stash` or any state-changing git besides your own commits.

## Review Focus

1. A redirect or a DNS answer that points at loopback or `169.254.169.254` is refused at dial time. It returns `target address not allowed`, not `up = 0`.
2. A slow target can never let probes pile up past 32, or let one tick's work outlast the next probe of the same check.
3. A host's address can be a hostname, an IPv4, an IPv6 literal (bracketed when building `host:port` and URLs), or empty. Each probe handles all four.
4. Adding, editing or deleting a synthetic definition leaves the agent's version unchanged, so the agent gets no re-push.
5. A probe whose context is cancelled during manager shutdown records nothing partial and does not block shutdown beyond its timeout.

---

### Task 1: Probe package — guard, tcp, icmp, dns

**Files:**
- Create in `internal/probe`:
  - `probe.go`: types and dispatch.
  - `guard.go`: the dial guard.
  - `tcp.go`, `icmp.go` and `dns.go`: one probe each.
  - Tests next to each.

**Interfaces (produced):**

```go
package probe

type Observation struct {
	Metric string
	Value  *float64
	Err    string
}

// Guard decides whether an IP may be dialled.
type Guard struct{ /* always-refused + extra CIDRs */ }
func NewGuard(extra []netip.Prefix) *Guard          // production: loopback, link-local, unspecified, multicast + extra
func NewGuardForTest(allowLoopback bool, extra []netip.Prefix) *Guard
func ParseCIDRs(list string) ([]netip.Prefix, error) // PULSIX_PROBE_DENY_CIDRS parser
func (g *Guard) Allowed(ip netip.Addr) bool
func (g *Guard) Dialer(timeout time.Duration) *net.Dialer // Control refuses with ErrNotAllowed

var ErrNotAllowed = errors.New("target address not allowed")

// Run probes address with plugin and params, already validated by internal/checks.
// It never returns a Go error: every outcome is in the observations.
func Run(ctx context.Context, g *Guard, plugin string, params map[string]any, address string) []Observation
```

`Run` dispatches `tcp`, `icmp` and `dns` in this task. Task 2 adds `http` and `tls`.

- [ ] **Step 1: Failing tests** (loopback listeners; guards from `NewGuardForTest(true, nil)` unless stated):
  - guard: `127.0.0.1`, `::1`, `169.254.169.254`, `fe80::1`, `0.0.0.0` and `224.0.0.1` are refused by `NewGuard(nil)`. `10.0.0.1` is allowed. An extra `10.0.0.0/8` refuses `10.1.2.3`. `ParseCIDRs(" 10.0.0.0/8, 192.168.0.0/16 ")` gives two prefixes, and `ParseCIDRs("10.0.0.0/33")` errors.
  - tcp:
    - a listener that writes `SSH-2.0-test\r\n` with `expect: "SSH-2.0"` gives `tcp.up=1`, `tcp.connect_ms` ≥ 0 and `tcp.banner_match=1`;
    - `expect: "nope"` gives `banner_match=0`;
    - a closed port gives only `tcp.up=0`;
    - `send: "PING\r\n"` reaches the listener;
    - with `NewGuard(nil)`, `127.0.0.1` gives three error observations (one per metric the params would produce) with Err `target address not allowed`, and no `up`;
    - an empty address gives errors with `host has no address`;
    - an IPv6 address `::1` works.
  - dns, against a stub UDP server on loopback that answers A `example.test` with `192.0.2.7` (`server: "127.0.0.1:<port>"`):
    - `dns.up=1` and `dns.resolve_ms` ≥ 0;
    - `expect: "192.0.2.7"` gives `answer_match=1`, and `expect: "192.0.2.8"` gives `answer_match=0`;
    - NXDOMAIN gives `dns.up=0`;
    - `name` with `{address}` substitutes the address;
    - a `server` the guard refuses gives `target address not allowed`.
  - icmp: if an unprivileged ICMP socket to `127.0.0.1` can be opened in the test environment, `count: 2` gives `icmp.up=1`, `loss_pct=0` and `rtt_ms` ≥ 0. Otherwise, `t.Skip` with the reason. A refused socket maps to one error per metric whose text names `net.ipv4.ping_group_range`. Test that mapping with an injected socket opener.
- [ ] **Step 2:** `go test -race -timeout 900s ./internal/probe/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
  - Hand-roll the DNS stub server in the test from the RFC 1035 wire format; it needs only A and NXDOMAIN, and avoids a new dependency.
  - Build DNS queries for the `server` case with `net.Resolver{PreferGo: true, Dial: guardedDial}`.
  - For `type`, use `LookupHost` filtered by family for A and AAAA, then `LookupCNAME`, `LookupMX`, `LookupTXT` and `LookupNS`.
  - Compare `expect` case-insensitively, without a trailing dot.
- [ ] **Step 4:** Run the same command, expect PASS, then lint.
- [ ] **Step 5:** Commit `feat(probe): tcp, icmp and dns probes behind a dial guard`.

### Task 2: Probe package — http, tls

**Files:**
- Create in `internal/probe`: `http.go`, `tls.go` and their tests.

**Interfaces:**
- Consumes `Guard`, `Observation` and `Run` from Task 1.
- Adds the `http` and `tls` cases to `Run`.

- [ ] **Step 1: Failing tests** (`httptest.NewServer` / `NewTLSServer`; guard allows loopback):
  - http:
    - 200 with body `ok healthy` and `body_contains: "healthy"` gives `http.up=1`, `status_code=200`, `response_ms` ≥ 0 and `body_match=1`;
    - 503 gives `up=1` and `status_code=503`;
    - `method: "HEAD"` sends HEAD;
    - `{address}` in `url` is substituted, and an IPv6 address is bracketed;
    - a redirect to `http://127.0.0.1:<port>/` under `NewGuard(nil)` gives `target address not allowed`;
    - `follow_redirects: false` reports the 302 itself;
    - more than 10 redirects gives `up=0`;
    - a self-signed TLS server gives `up=0` plus an error naming the certificate when `verify_tls` is true, and `up=1` when it is false;
    - a body over 1 MiB is read only to 1 MiB, and a match past it is `body_match=0`;
    - a connection refused gives `up=0`.
  - tls:
    - against `NewTLSServer`: `tls.up=1`, `handshake_ms` ≥ 0, `cert_valid=0` (self-signed), and `cert_expiry_days` within ±1 of the test cert's NotAfter;
    - against a server whose cert is signed by a test CA in an injected root pool and matches `server_name`: `cert_valid=1`;
    - an expired leaf gives a negative `cert_expiry_days`;
    - a refused connection gives only `tls.up=0`.
- [ ] **Step 2:** `go test -race -timeout 900s ./internal/probe/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
  - The TLS root pool is injectable for tests (an unexported field, or a constructor option); production uses the system roots.
  - `tls` connects with `InsecureSkipVerify` and verifies the chain itself, so expiry is reported even for an invalid chain.
- [ ] **Step 4:** Run the same command, expect PASS, then lint.
- [ ] **Step 5:** Commit `feat(probe): http and tls probes`.

### Task 3: Checks — plugins, validation, resolver split, API, catalogue

**Files:**
- `internal/checks/definitions.go`:
  - plugin constants and the known-plugins set;
  - `validateParams` for the five, per the spec's table and "Validation" section;
  - `IsSynthetic(plugin string) bool`.
- `internal/checks/resolver.go`:
  - `ResolvedSet` gains `Synthetic []Resolved`, sorted by Key;
  - `toResolvedSet` puts synthetic plugins there, and `Checks` and `Version` cover only the rest.
- `internal/checks/items.go`: none of the five have an item list. Confirm nothing there needs a case.
- `internal/shared/service.go`: the plugin allow-list.
- `internal/api`: the `enum` tags in `check_handlers.go`, `host_check_handlers.go` and `shared_handlers.go`.
- Regenerate `web/src/api/schema.d.ts`.
- `internal/rules/catalog.go`: one entry per new metric.
  - CheckKey is the plugin name.
  - Kind: `MetricNumeric` for every metric.
- Tests in each package.

**Interfaces (produced):** `checks.IsSynthetic(plugin string) bool`, `ResolvedSet.Synthetic []Resolved`.

- [ ] **Step 1: Failing tests:**
  - validateParams accepts each spec default set. It refuses:
    - `tcp` without `port`, and `port: 70000`;
    - `http` with `url: "ftp://x"`, and `method: "POST"`;
    - `dns` with `type: "SRV"`, and `server: "not-an-ip"`;
    - `icmp` with `count: 11`;
    - an unknown key on each plugin.
  - A host with an org-bound `cpu` and a host-bound `http`:
    - `Checks` holds only cpu, and `Synthetic` holds only http;
    - `Version` equals the version of the same host without the http definition (Review Focus 4).
  - The catalogue has the 15 new metrics.
  - The API accepts `plugin: "http"` with valid params, and refuses invalid ones with 422.
- [ ] **Step 2:** Run the affected packages (DB-backed ones one at a time). Expected: FAIL.
- [ ] **Step 3:** Implement, then run `npm run api:gen` from `web/`.
- [ ] **Step 4:** Run the packages again (`internal/api` with `-timeout 1800s`), then lint. Expected: PASS.
- [ ] **Step 5:** Commit `feat(checks): accept the synthetic plugins and keep them off the agent`.

### Task 4: Manager probe runner

**Files:**
- Create in `internal/probes`:
  - `runner.go`: the loop.
  - `ports.go`: the interfaces the loop depends on.
  - Tests with fakes.
  - One integration test against `internal/db/testdb`.
- Modify:
  - `internal/config/config.go`: `ProbeDenyCIDRs []netip.Prefix`, parsed from `PULSIX_PROBE_DENY_CIDRS` with `probe.ParseCIDRs`.
  - `internal/app/app.go`: wire the runner with `startLoop`, and stop it on shutdown alongside the other loops.

**Interfaces:**
- Consumes:
  - `probe.Run`, `probe.NewGuard` and `probe.ParseCIDRs` (Task 1);
  - `checks.ResolvedSet.Synthetic` (Task 3);
  - `checks.Service.ResolveOrg`;
  - `checks.Service.RecordObservations`.
- Produces:
  - `probes.NewRunner(deps Deps) *Runner`, where `Deps` holds:
    - `Orgs`: lists org ids;
    - `Resolver`: `ResolveOrg`;
    - `Hosts`: address and `monitored` per host id of an org;
    - `Recorder`: `RecordObservations`;
    - `Locker`: the `schedule.Locker` shape;
    - `Probe`: `func(ctx, plugin, params, address) []probe.Observation`, defaulting to `probe.Run` with the guard;
    - `Clock func() time.Time`;
    - `Tick`: default 5 s;
    - `Refresh`: default 30 s;
    - `MaxConcurrent`: default 32;
    - `Log`.
  - `(*Runner).Run(ctx context.Context)`.
  - Reuse existing store methods for the host and org lookups where they exist (search `internal/hosts` and `internal/tenancy`). Add a narrow query only if none fits.

- [ ] **Step 1: Failing tests (fakes, injected clock):**
  - Without the lock, nothing is probed.
  - With it, a host's two synthetic checks at 60 s and 300 s are probed at t=0. At t=60 the 60-s check is probed again and the 300-s check is not.
  - Results are recorded with that host's id, the check key, an `At` from the clock, and item `""`.
  - A probe that blocks never leads to a second concurrent probe of the same (host, key). Total concurrency never exceeds `MaxConcurrent`; use a fake probe that counts in-flight calls.
  - A definition removed at refresh is not probed after the next refresh, and a new one is probed within one refresh.
  - A host that is no longer `monitored` stops being probed.
  - A cancelled context returns from `Run` within the probe timeout, and records nothing for a probe cut short (Review Focus 5).
  - The timeout passed to the probe is `min(timeout_s, interval − 1s)`.
- [ ] **Step 2:** `go test -race -timeout 900s ./internal/probes/`. Expected: FAIL.
- [ ] **Step 3:** Implement.
  - Hold the lock for as long as `Run` runs: `TryLock` every tick, `Unlock` on exit.
  - Keep the next-due map in memory, keyed by host and check key.
- [ ] **Step 4: Integration test** (`internal/probes`, testdb):
  - an org with a monitored host whose address is a loopback `httptest` server;
  - a host-bound `http` definition;
  - the runner uses a guard that allows loopback, and runs one tick;
  - `check_current` then holds `http.up=1` for that host and key;
  - a second runner on another locker for the same key does not probe while the first holds the lock.
- [ ] **Step 5:** Run the package tests, then `internal/app` and `internal/config`, then lint. Expected: PASS.
- [ ] **Step 6:** Commit `feat(probes): the manager runs synthetic checks`.

### Task 5: SPA

**Files:**
- `web/src/features/checks/CheckDialog.tsx`, `web/src/features/instance/shared/SharedCheckFormPage.tsx`:
  - add the five plugins;
  - add a parameter section per plugin with the spec's fields:
    - number inputs with min and max;
    - selects for `method` and `type`;
    - checkboxes for `follow_redirects` and `verify_tls`;
    - text inputs for the rest.
  - Send only the fields that are set. Leave out the defaults the user did not change; the server applies them.
  - Show the hint "runs from the Pulsix server" for the five.
- If the two forms would duplicate a large block, extract the synthetic parameter fields into one component in `web/src/features/checks/` and use it from both.
- i18n en/de:
  - plugin labels, field labels and hints, the hint above;
  - metric labels, wherever the SPA labels metrics (`hostChecks.ts`, `RuleDialog.tsx`; follow 3.7's commit `d4728a8c`).
- Tests next to each.

- [ ] **Step 1: Failing tests:**
  - The dialog offers the five plugins.
  - `http` with url and `body_contains` sends `{url, body_contains}`.
  - `tcp` without a port does not submit and shows the field error.
  - `dns` with `type` AAAA sends `{type: "AAAA"}`.
  - Editing an existing `http` check pre-fills its fields.
  - The shared form behaves the same.
- [ ] **Step 2–4:** Implement, then run the SPA checks.
- [ ] **Step 5:** Commit `feat(web): synthetic checks in the check forms`.

### Task 6: Backlog, upgrades, compose

- `docs/BACKLOG.md`:
  - 3.13 is `done`, with a `**Done 2026-11-11.**` note naming the five plugins, the manager vantage point and the dial guard;
  - a new row 3.13b, `SNMP checks`, `planned`, saying why it was split (credentials need secret references, see 3.5 and 3.6);
  - 3.6h and 3.13a stay planned, with a sentence each on what 3.13 already gives them.
- `docs/upgrades.md`: one newest-first bullet:
  - the new plugins run on the manager;
  - agents need no update;
  - `PULSIX_PROBE_DENY_CIDRS`;
  - the ICMP sysctl.
- `deploy/compose/README.md` and `.env.example`: `PULSIX_PROBE_DENY_CIDRS`, with the recommendation to add the stack's own network.
- Commit `docs: close backlog 3.13`.

The controller updates the end-user docs in the noix-docs scratch clone: `reference/check-plugins.md` and `user/monitoring.md`.
