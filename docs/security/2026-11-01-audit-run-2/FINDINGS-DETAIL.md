# Findings detail — confirmed medium, high and critical

## Any enrolled agent can trigger unlimited organization-wide resolves against the shared database by sending heartbeats in a loop

Fingerprint `internal/agentapi/stream.go:processHeartbeat:unthrottled-org-wide-resolve-per-heartbeat` — severity **medium**

Every Heartbeat message on an AgentStream runs a full, uncached checks.ResolveHost for the agent's organization before the version is compared. That means a Touch UPDATE, a SELECT * of every host row in the organization (MonitoredHosts -> ListAllHosts, used only to filter monitored ids), ListDefinitions, the monitoring-group list, and one selector query per monitoring group (QueryHosts, a DISTINCT ON CTE over the organization's host_tags joined to its hosts). The server sets no limit on how often one agent identity may send heartbeats. There is no per-stream or per-serial message rate limit, no bundle cache (backlog 3.19 is still 'idea'), and no cap on how many streams one serial may hold open: StreamRegistry.track adds every stream to byHost, and grpc.NewServer sets no MaxConcurrentStreams or tap handle. The intended 30 s cadence lives only in the honest agent's own ticker. A small protobuf message therefore costs 4+G statements (Touch plus 3+G resolve reads). Each read grows with the organization's host and tag count, and H, T and G are writable by operator+ without quota. All of these statements run on the single pgx pool and manager process that every tenant shares. A local reproduction with the real checks.Service sent 600 heartbeats, 200 on each of 3 concurrently open streams of one enrolled agent, in an organization with 10 groups. Every heartbeat was acknowledged within about 17 ms in total, with no throttling, and they caused 7800 resolve-time store or port calls (13 per heartbeat). The owner's own backlog 3.19 measurement of 28.3 ms per resolve at 1000 hosts and 5 groups puts one looping stream at roughly one saturated core. A handful of streams can therefore keep connections in the shared pool busy continuously.

### Trace

1. **entrypoint** `internal/agentapi/stream.go:92` (Service.receiveMessages): The mTLS-authenticated agent's AgentStream receive loop reads each AgentMessage, with no rate, interval or count check. It then hands the message to s.process at line 102.
2. **propagation** `internal/agentapi/stream.go:137` (Service.process): A Heartbeat body is routed to processHeartbeat (line 138).
3. **propagation** `internal/agentapi/stream.go:173` (Service.processHeartbeat): After the Touch UPDATE (line 167), ResolveHost runs unconditionally on every heartbeat, before the version comparison at line 195.
4. **propagation** `internal/checks/resolver.go:324` (Service.ResolveHost): MonitoredHosts reads every host of the organization, through hosts.ListAllHosts (SELECT * FROM hosts WHERE org_id=$1). It then calls loadResolveBundle.
5. **propagation** `internal/checks/resolver.go:137` (Service.loadResolveBundle): After ListDefinitions (line 89) and List groups (line 93), the loop issues one MatchingHosts selector query per monitoring group. Nothing is cached.
6. **sink** `internal/hosts/tags_store_sql.go:122` (SQLStore.QueryHosts): Per group, a DISTINCT ON CTE runs over the organization's host_tags plus the matched host rows. It executes on the shared pool.

### Evidence

- `internal/grpcsrv/server.go:176`: grpc.NewServer sets only creds, message-size limits, auth interceptors and keepalive MaxConnectionAge. It has no MaxConcurrentStreams, InTapHandle or per-identity limiter for agent streams.
- `internal/agentapi/registry.go:280`: StreamRegistry.track adds every new stream of a host to byHost[hostID] with no cap. The local run kept 3 streams of one serial tracked at once.
- `internal/app/checks_ports.go:87`: MonitoredHosts loads every host of the organization in full, through ListAllHosts, only to filter the monitored ids.
- `internal/db/queries/hosts.sql:102`: ListAllHosts is an unpaginated SELECT * over the organization's hosts, including the facts and vars JSON columns.
- `internal/db/db.go:56`: A single pgxpool serves all tenants. Code sets no pool size and no statement timeout, so the pgx defaults apply unless the DSN sets them.
- `internal/agentapi/stream.go:166`: processHeartbeat's own comment acknowledges the organization-wide resolve on every heartbeat (backlog 3.19).
- `docs/BACKLOG.md:340`: Backlog 3.19 ('idea') records the owner's measurement of 28.3 ms per resolve at 1000 hosts and 5 groups, framed as fleet-scale cost. It does not address an agent that heartbeats faster than its 30 s ticker.
- `internal/agentapi/checks_test.go:206`: The existing test asserts one ResolveHost per heartbeat, even for heartbeats that report the converged version.

### Principal, resource and conditions

- Attacker: One enrolled agent identity sends Heartbeat messages back to back on several concurrently open AgentStreams, instead of one every 30 s.
- authentication_level: The attacker holds a valid, unrevoked agent certificate for one host. That means root on any monitored host of the organization, which is a lower-trust principal with no REST access, or any user who can enroll an agent: operator+ in an organization, and any MFA-complete user can create their own organization.
- network_routing: The attacker must be able to reach the manager's gRPC listener (8444), which agents need anyway. The default compose file does not publish it on the host, but any deployment with remote agents does.
- data_state: The per-heartbeat cost grows with the organization's host, tag and monitoring-group counts. The same tenant can raise all three without quota.
- system_configuration: How far the load spreads to other tenants depends on the deployment's pool size (the pgx default unless the DSN sets one) and on any DSN-level statement_timeout. Code sets neither.

### Bounded local reproduction

Inputs:
- `AgentMessage{Heartbeat{version:"x", config_version:"anything"}}, sent 200 times on each of 3 concurrently open AgentStreams of one agent, in an organization with 10 monitoring groups`

Steps:
1. Copied the repository to scratch and added internal/agentapi/zz_v13_heartbeat_test.go. It uses a real checks.Service (checks.NewService with WithHostRoster and WithMatcher) backed by counting fakes for Store.ListDefinitions, Store.List (10 groups), HostRoster.MonitoredHosts (returns the enrolled host) and HostMatcher.MatchingHosts.
2. Enrolled one agent through the package's own test harness (newHarnessPort, enrollAnAgent). Opened 3 AgentStreams, kept them all open, sent 200 heartbeats on each and read each acknowledgement. Then read the StreamRegistry's tracked-stream count for the host.
3. Ran it in the approved sandbox: go test -count=1 -v -run 'TestV13HeartbeatResolveFanOut|TestConvergenceUnrecognisedVersionPushesOnceThenConverges' ./internal/agentapi/ (no network, read-only source and module cache, 2 CPU / 2 GiB, 300 s limit).

Observed: concurrently tracked streams for this one host/serial: 3. streams=3 heartbeats_per_stream=200 acked=600 groups=10 elapsed=16.744667ms. downstream: MonitoredHosts=600 ListDefinitions=600 ListGroups=600 MatchingHosts=6000 total=7800 (3+G=13 per heartbeat). No heartbeat was throttled or refused. The repository's own TestConvergenceUnrecognisedVersionPushesOnceThenConverges also passed, asserting one resolve per heartbeat. The Touch UPDATE on each heartbeat ran against a fake store, so it is not counted in the 7800.

Invariant proven: The server should bound the work one agent identity can cause on the shared pool, rather than relying on the agent to behave. Resolves should be rate-limited or coalesced per stream or serial and per organization, for example a minimum interval between resolves per stream answered from the last result, plus a short-TTL per-organization bundle cache. The number of concurrent AgentStreams per agent serial should also be capped.

### Remediation

Bound resolve work per agent identity on the server. In processHeartbeat, if this stream resolved less than a minimum interval ago (for example 10 s), skip the resolve and answer from the last ResolvedSet. Put a short-TTL per-organization bundle cache in front of loadResolveBundle (backlog 3.19's named mitigation), so concurrent heartbeats of one organization share one load. Cap concurrent AgentStreams per agent serial in StreamRegistry.track: refuse, or close the oldest stream, beyond a small number such as 2. Optionally set grpc.MaxConcurrentStreams. Add a regression test showing that 100 heartbeats inside one interval cause at most one ResolveHost, and that a third stream for one serial is refused.

`internal/agentapi/stream.go`:

```go
// in processHeartbeat, after Touch and before ResolveHost:
if last, ok := tracked.lastResolve(); ok && s.now().Sub(last.at) < minResolveInterval {
	if last.set.Version == hb.GetConfigVersion() {
		return nil, nil
	}
	return checkConfigFor(last.set)
}
resolved, err := s.deps.Checks.ResolveHost(ctx, agent.OrgID, agent.HostID)
// ... on success: tracked.rememberResolve(resolved, s.now())
```

`internal/agentapi/registry.go`:

```go
// in track: refuse beyond maxStreamsPerSerial
if countSerial(r.byHost[hostID], serial) >= maxStreamsPerSerial {
	return nil, nil, errTooManyStreams // AgentStream maps to codes.ResourceExhausted
}
```

## Second-factor guessing on /auth/totp/verify is limited only per source address, so a password holder can brute-force the TOTP code

Fingerprint `internal/api/mfa_handlers.go:totpVerify:no-per-account-second-factor-attempt-limit` — severity **medium**

POST /api/v1/auth/totp/verify is the only step between a password-only (pending) session and an MFA-complete session for a TOTP account. The only bound on wrong codes is the per-address auth limiter: by default 20 failures per minute per IPv4 address or per IPv6 /64, per endpoint. Nothing counts failures per account or per session, and a wrong code neither revokes nor marks the pending session. An attacker who has the victim's password can log in (successful logins are refunded by the limiter), keep the pending session for up to 12 h (renewable by logging in again), and send six-digit guesses from as many source keys as they control. Three codes are valid at any instant (current step plus or minus one), so each guess succeeds with probability about 3e-6. One IPv4 address gives 28,800 guesses a day, about an 8% chance per day; a routed IPv6 /56 (256 /64 keys) gives 5,120 guesses a minute and an expected success in about an hour. A correct guess upgrades the session to MFA-complete with a fresh cookie, which is full account takeover including the victim's org roles and any instance-admin flag. Backlog 7.8 fixed the run-1 concurrency bypass and explicitly lists a per-account second-factor failure counter as not done.

### Trace

1. **entrypoint** `internal/api/auth_handlers.go:96` (POST /api/v1/auth/login): Attacker with the victim's password obtains a pending (mfa_complete=false) session cookie via Auth.Login (sessions.Create(..., false)); a successful login is refunded by the limiter and can be repeated to renew the 12 h session.
2. **propagation** `internal/api/auth_rate_limit.go:81` (authRateLimitMiddleware): lim.Reserve(ip) is the only budget check: keyed on the resolved client address alone, so each new IPv4 address or IPv6 /64 starts a fresh 20/min window for the same session and account.
3. **propagation** `internal/api/mfa_handlers.go:169` (POST /api/v1/auth/totp/verify): requireSession admits the pending session; verifyFactorCode checks the guess and returns 401 on a wrong code with no per-user or per-session failure accounting and no session revocation.
4. **propagation** `internal/auth/totp.go:285` (TOTP.verifyAndConsume): matchStep accepts the code of the current step or either neighbouring step; a wrong code returns (false, nil) and nothing is recorded.
5. **sink** `internal/api/mfa_handlers.go:172` (Sessions.MarkMFAComplete): The first correct guess upgrades the attacker's session to MFA-complete and returns its new session cookie.

### Evidence

- `internal/clientip/limiter.go:165`: key(): IPv4 per address, IPv6 per /64; the only dimension the auth limiter counts on.
- `internal/config/config.go:121`: DefaultAuthRateLimit = 20 failed attempts per minute per address per endpoint.
- `internal/auth/totp.go:26`: totpSkewSteps = 1: three six-digit codes are valid at any instant.
- `internal/db/queries/auth.sql:83`: SetTOTPLastStep is the only TOTP write on the verify path, and only on success; no failure counter column or query exists.
- `internal/auth/session.go:21`: SessionTTL = 12 h: lifetime of the pending session the guesses run against.
- `docs/BACKLOG.md:547`: Backlog 7.8 closure note: 'Not done: a per-account second-factor failure counter independent of the address.'

### Principal, resource and conditions

- Attacker: Holder of a dummy victim's password but not the authenticator, sending /auth/totp/verify guesses for one pending session from many source addresses.
- authentication_level: Attacker knows the victim's password (phished, reused or leaked) and can open a pending session; no second factor is known.
- network_routing: Guess rate scales with distinct source keys the manager sees: one IPv4 address gives 20/min (about 8%/day); an IPv6 /56 or a pool of addresses gives thousands per minute. Requires the manager to see the true client address (direct exposure, or PULSIX_TRUSTED_PROXIES set for the fronting proxy); behind an untrusted proxy all callers share one key and the global rate is 20/min.
- data_state: Victim account uses TOTP as its second factor. A passkey-only account has no TOTP secret and Verify always fails; recovery codes are 80-bit and not guessable.
- timing_dependency: Success is probabilistic per guess (about 3e-6); expected effort is about 333k guesses.

### Bounded local reproduction

Inputs:
- `POST /api/v1/auth/totp/verify  Content-Type: application/json  body {"code":"NNNNNN"} with RemoteAddr 192.0.2.10:4000 (21 wrong codes)`
- `POST /api/v1/auth/totp/verify  Content-Type: application/json  body {"code":"NNNNNN"} with RemoteAddr [2001:db8:<k>::1]:4000, k = 0..100, 20 sequential codes per /64`
- `Harness: scratch copy internal/api/zz_v04_audit_internal_test.go, TestV04TOTPGuessBudgetPerAddressOnly (real registerMFARoutes + withAuthRateLimit with AuthRateLimit=20 + real auth.TOTP and auth.Sessions; fake TOTPStore with a confirmed dummy secret, fake gen.DBTX answering MarkSessionMFAComplete with UPDATE 1, fixed clock)`

Steps:
1. Copy the repository to scratch, move the other internal/api *_test.go files and internal/api/apitest out of the scratch copy (their testcontainers dependency is not in the read-only module cache), and add the harness test file.
2. Run in the approved sandbox: docker create --pull never --network none --read-only --cap-drop ALL --security-opt no-new-privileges --user 65532:65532 ... golang:1.27.1-alpine go test -count=1 -v -run 'TestV04' ./internal/api/
3. The harness injects one pending principal, sends 21 wrong codes from one IPv4 address (expect 20x401 then 429), then walks sequential codes starting 2000 below the current valid code, 20 per distinct IPv6 /64, against the same pending session.

Observed: phase1 one IPv4 address, 21 wrong codes: status counts map[401:20 429:1]; phase2 distinct /64s: 101, guesses: 2001, refused(429): 0, success at guess #2001 (valid code), MarkSessionMFAComplete UPDATEs: 1, SetTOTPLastStep calls: 1; success Set-Cookie contains pulsix_session: true (agents/v04-session-auth/artifacts/evidence.txt).

Invariant proven: Wrong second-factor attempts against one account (or one pending session) should be bounded regardless of how many source addresses send them: after a small number of consecutive failures the pending session is revoked and further verification for that user is locked or exponentially delayed, so a stolen password alone cannot be turned into an MFA-complete session by guessing.

### Remediation

Add a per-user (or per pending session) failed second-factor counter in the database (additive migration) and check it before verifying. Increment it atomically on every failed TOTP or recovery-code check through verifyFactorCode, on the verify and the disable path. Past a small threshold (for example 5-10), revoke the pending session and lock verification for that user with exponential backoff; reset on success. Keep the per-address limiter as the outer layer and audit-log lockouts.

`internal/db/migrations/000NN_mfa_failures.sql`:

```go
ALTER TABLE users ADD COLUMN mfa_failed_attempts integer NOT NULL DEFAULT 0, ADD COLUMN mfa_locked_until timestamptz;
```

`internal/api/mfa_handlers.go`:

```go
// in /auth/totp/verify, before verifyFactorCode:
if locked, err := deps.TOTP.Locked(ctx, p.UserID); err != nil { return nil, mapAuthErr(ctx, err) } else if locked { return nil, huma.Error429TooManyRequests("too many failed attempts") }
if err := verifyFactorCode(ctx, deps, p.UserID, in); err != nil {
	if n, _ := deps.TOTP.RecordFailure(ctx, p.UserID); n >= maxSecondFactorFailures { _ = deps.Sessions.Revoke(ctx, p.SessionID) }
	return nil, err
}
_ = deps.TOTP.ResetFailures(ctx, p.UserID)
```

## Unauthenticated POST /api/v1/setup/complete runs a 64 MiB argon2id hash on every call, even after setup is complete, with no rate or concurrency limit

Fingerprint `internal/setup/service.go:Complete:argon2id-before-already-completed-check` — severity **medium**

The completion route for the first-run wizard stays registered and public for as long as the instance runs. setup.Service.Complete hashes the supplied admin password with argon2id (t=3, m=64 MiB, p=2) before it checks whether an instance administrator already exists. The route also has none of the per-address limiters that the other authentication routes use. On a completed instance, any anonymous client can send a small (~200 byte) well-formed JSON body. Each request is refused with 409, but only after the manager has allocated 64 MiB and spent roughly 120-260 ms of argon2id work on two threads. Live heap and CPU grow linearly with concurrent requests. Neither the HTTP server, the route nor the service bounds that concurrency, so a single client can degrade or exhaust the memory and CPU of the shared manager process. That process serves every organization's REST API, logins, the SPA and the gRPC worker/agent endpoint.

### Trace

1. **entrypoint** `internal/api/setup_handlers.go:81` (registerSetupRoutes: POST /api/v1/setup/complete): Public route on the root huma API (registered at internal/api/api.go:503). It needs no session or bearer token. Body validation checks only the shape: password minLength 12, email format, slug pattern.
2. **propagation** `internal/api/setup_handlers.go:90` (setup complete handler): Passes the anonymous body straight to deps.Setup.Complete.
3. **propagation** `internal/setup/service.go:234` (Service.Complete): in.validate() is the only check before hashing, and any well-formed body passes it.
4. **sink** `internal/setup/service.go:237` (Service.Complete): auth.HashPassword runs argon2.IDKey with 64 MiB of memory, 3 passes and 2 threads (internal/auth/password.go:16-18,35). This happens before the InTx/AdminExists check at lines 243-250 returns ErrAlreadyCompleted, which the handler maps to 409 at setup_handlers.go:279-280.

### Evidence

- `internal/setup/service.go:237`: hash, err := auth.HashPassword(in.AdminPassword) comes before s.store.InTx (line 243) and the tx.AdminExists check (lines 244-249). No AdminExists check runs before the hash.
- `internal/auth/password.go:17`: argonMemory = 64 * 1024 KiB (64 MiB), with argonTime = 3 and argonThreads = 2. argon2.IDKey is called at line 35.
- `internal/api/setup_handlers.go:107`: The route's only options are DefaultStatus and withAudit. withAuthRateLimit appears only at auth_handlers.go:106, mfa_handlers.go:178, passkey_handlers.go:218 and member_handlers.go:264.
- `internal/api/api.go:371`: The router-wide middleware is RequestID, Logger, Recoverer, securityHeaders, the session cookie and requireMFAForExplorer. None of them throttles requests or bounds concurrency.
- `cmd/manager/main.go:210`: The http.Server sets only ReadHeaderTimeout. Nothing caps connections or request concurrency in front of the handler.
- `deploy/compose/docker-compose.yml:90`: The compose stack publishes manager port 8080 directly. The repository defines no container memory limit, no GOMEMLIMIT and no proxy rate-limit or in-flight middleware.

### Principal, resource and conditions

- Attacker: An anonymous network client able to send HTTP POST requests to the manager.
- authentication_level: None. The route is public and needs no session, token or secret.
- network_routing: The attacker can reach the manager's HTTP listener, which the compose stack publishes on 8080, or reach it through the optional proxy. Any limits in a reverse proxy or gateway that the operator adds are outside the repository.
- data_state: Applies to every instance for its whole lifetime. On a completed instance, every call is refused with 409 after the hash, so the only effect is resource consumption.

### Bounded local reproduction

Inputs:
- `POST /api/v1/setup/complete {"admin_email":"a@x.test","admin_password":"aaaaaaaaaaaa","admin_name":"A","org_slug":"a","org_name":"A"}`
- `setup.Service.Complete(ctx, setup.Input{AdminEmail: "a@x.test", AdminPassword: "aaaaaaaaaaaa", AdminName: "A", OrgSlug: "a", OrgName: "A"}) against a store whose AdminExists returns true, 1 call and then 3 concurrent calls`

Steps:
1. Copied the repository into the verifier scratch directory. Removed store_sql_test.go from the copy only, because it needs testcontainers. Added internal/setup/audit_v07_test.go, which defines its own concurrency-safe completedStore: AdminExists always returns true, and the store counts AdminExists calls and writes. The test needs no database and does not reuse the hunter's harness.
2. The test measures runtime.MemStats.TotalAlloc and wall time for Status, which is the cheap baseline, and for Complete with the payload body: one call, then 3 concurrent goroutines. It asserts that every Complete returns ErrAlreadyCompleted, that each call allocates at least 60 MiB, and that the store sees zero writes.
3. Ran it in the approved Docker sandbox: no network, read-only root and source, cap-drop ALL, 2 CPUs, 2 GiB memory, 512 pids, 300 s timeout. Command: go test -count=1 -v -run TestAuditV07CompleteOnCompletedInstance ./internal/setup/.
4. The sandbox run's output is the audit artifact agents/v07-availability/artifacts/evidence.txt (not a repository source). Its line 3 records Complete x1 allocating 67154904 bytes (64.0 MiB) in 120 ms, and its line 4 records x3 concurrent allocating 201417016 bytes (192.1 MiB, HeapSys 199.7 MiB). Every call returned ErrAlreadyCompleted with zero writes, and Status allocated 584 bytes.

Observed: Status: 584 bytes, ~18 us. Complete x1: 67154904 bytes (64.0 MiB) allocated, 120 ms, ErrAlreadyCompleted. Complete x3 concurrent: 201417016 bytes (192.1 MiB) allocated, HeapSys 199.7 MiB, 334 ms, all ErrAlreadyCompleted. AdminExists was called 5 times (1 Status + 4 Complete, each after its hash), with 0 writes. PASS. This matches the hunter's independent run: 64.0 MiB per call, and 256.1 MiB for 4 concurrent calls.

Invariant proven: Once setup is complete, the route must refuse cheaply, as Complete's own doc comment promises for a completion 'discovered before this call started' (service.go:226). A public route that can reach argon2id must also sit behind a per-address limit and a concurrency bound before it hashes.

### Remediation

First, refuse a completed instance before hashing. Add a cheap s.store.AdminExists check before auth.HashPassword and keep the in-transaction re-check for the concurrent first-run race. Second, attach withAuthRateLimit to POST /api/v1/setup/complete; a refused 409 is a 4xx, so it keeps its slot. Third, as defence in depth for every route that reaches argon2id (login also runs a dummy hash for unknown e-mails), bound concurrent argon2id work. Use a semaphore of about runtime.NumCPU() slots that the password hasher owns and that is injected through the constructor, in line with the repository's no-package-state rule. Regression test: on a store whose AdminExists returns true, Complete returns ErrAlreadyCompleted without hashing. Assert this through TotalAlloc staying under 1 MiB or through a hasher seam.

`internal/setup/service.go`:

```go
func (s *Service) Complete(ctx context.Context, in Input) (Result, error) {
	if err := in.validate(); err != nil {
		return Result{}, err
	}
	// Refuse a completed instance before spending argon2id on an
	// anonymous body; the AdminExists re-check inside InTx below still
	// closes the concurrent first-run race.
	exists, err := s.store.AdminExists(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("check admin exists: %w", err)
	}
	if exists {
		return Result{}, ErrAlreadyCompleted
	}
	hash, err := auth.HashPassword(in.AdminPassword)
	if err != nil {
		return Result{}, fmt.Errorf("hash admin password: %w", err)
	}
	// ... unchanged InTx with the AdminExists re-check ...
}
```

`internal/api/setup_handlers.go`:

```go
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAuthRateLimit(deps), withAudit("completeSetup", "setup"))
```

## A managed host's returned facts can replace the pinned SSH options (and add a ProxyCommand) through pulsix_ssh_common_args

Fingerprint `internal/worker/runner/inventory.go:forcedConnectionVars:pinned-ssh-args-shadowed-by-host-facts` — severity **medium**

The worker enforces host-key pins (and the aliased jump chain) by moving each host's SSH common args into the inventory host var pulsix_ssh_common_args. It then forces the extra var ansible_ssh_common_args to the template "{{ pulsix_ssh_common_args | default('') }}". The extra var has the highest precedence, but the variable it reads is an ordinary inventory host var. In Ansible, host facts outrank inventory host vars. The run's ansible.cfg and subprocess env leave inject_facts_as_vars at its default of True, so any module result's ansible_facts become top-level host vars. ansible-core's clean_facts drops facts named like connection variables (ansible_ssh_*, ansible_connection, interpreter and become names), but it does not know the pulsix_ name. A managed host controls the JSON its modules return, because its own python runs them. A compromised managed host can therefore return ansible_facts {"pulsix_ssh_common_args": "-o StrictHostKeyChecking=no -o ProxyCommand=<cmd>"}. A TOFU-accepted impostor of an unpinned host can do the same. That includes the setup module's output when a play gathers facts. On that host's next SSH connection in the same run, the forced template resolves to the attacker's value, the pinned StrictHostKeyChecking=yes/HostKeyAlias options disappear, and ssh runs the ProxyCommand on the worker as the ansible-playbook uid (the play uid in production). That uid can read and write the run's work dir through the group bits ShareWorkDir mirrors: keys/ with every run credential's key file, inventory.json, vault/ password files, extravars and the shared known_hosts. It can read the credentials the run uses for other hosts. It can also rewrite known_hosts to drop other hosts' pins for the rest of the run. This was reproduced with the worker image's ansible-core 2.21.4. A fact returned by a module replaced the pinned options on the next ssh command line, and the ProxyCommand created a marker file on the controller. With ANSIBLE_INJECT_FACT_VARS=False, or with the carrier renamed to an ansible_ssh_-prefixed name, the pins were kept and no command ran.

### Trace

1. **entrypoint** `internal/worker/runner/runner.go:191` (Runner.Run): The worker starts ansible-runner/ansible-playbook against the claim's managed hosts. The JSON of every module result, including ansible_facts (and the setup module's facts when a play gathers them), comes from the managed host's python, which a compromised or impostor host controls. ansible-core merges these facts into that host's variables.
2. **propagation** `internal/worker/runner/inventory.go:480` (pinHosts): For a pinned host, the options '-o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-<id>' are prepended to its ansible_ssh_common_args (which also carries the aliased ProxyJump chain) and stored at line 484.
3. **propagation** `internal/worker/runner/inventory.go:386` (moveCommonArgs): The value moves into the inventory host var pulsix_ssh_common_args, and ansible_ssh_common_args is deleted from the inventory (called at inventory.go:229). An inventory host var ranks below host facts.
4. **propagation** `internal/worker/runner/layout.go:122` (ansibleCfgText): The generated ansible.cfg sets host_key_checking, retry_files_enabled, local_tmp and ssh_args but not inject_facts_as_vars. The subprocess env (runner.go:481-486) does not set ANSIBLE_INJECT_FACT_VARS either. `ansible-config dump` with this cfg in the worker image reports INJECT_FACTS_AS_VARS(default) = True.
5. **sink** `internal/worker/runner/inventory.go:368` (forcedConnectionVars): The forced extra var ansible_ssh_common_args = "{{ pulsix_ssh_common_args | default('') }}" is templated per task with the host's variables, facts included. A fact of that name replaces the pinned options on the ssh command line, and a ProxyCommand in it runs on the worker. This was observed with ansible-core 2.21.4.

### Evidence

- `internal/worker/runner/inventory.go:349`: pinnedCommonArgsVar = "pulsix_ssh_common_args" is the host var that carries the pinned options and the jump chain.
- `internal/worker/runner/inventory.go:357`: The design comment says that what stays possible is a playbook redefining pulsix_ssh_common_args (by design, spec §14). It does not consider facts returned by managed hosts.
- `internal/worker/runner/inventory.go:279`: The worker's reserved-name guard applies only to the claim's extra vars. Nothing checks module-returned facts.
- `internal/worker/runner/layout.go:123`: The [defaults] section has host_key_checking = True but no inject_facts_as_vars = False.
- `internal/worker/runner/runner.go:481`: The subprocess env adds ANSIBLE_HOST_KEY_CHECKING, ANSIBLE_RETRY_FILES_ENABLED, ANSIBLE_SSH_ARGS, ANSIBLE_CONFIG, ANSIBLE_LOCAL_TEMP and HOME, and nothing that disables fact injection. stripPrivateEnv (runner.go:805) removes any inherited ANSIBLE_* setting.
- `internal/worker/runner/share.go:31`: ShareWorkDir mirrors the owner bits onto the group so the play uid can read and write the run's files (keys/, inventory.json, vault/, env/extravars, known_hosts; names at layout.go:19-58). That is what a ProxyCommand running as the play uid can reach.
- `internal/ansiblevars/ansiblevars.go:44`: The pulsix_ prefix is forbidden only for user input (template/webhook extra vars, schema fields). Facts that managed hosts return are never checked.

### Principal, resource and conditions

- Attacker: A compromised managed host (or a TOFU impostor of an unpinned host) in a Pulsix organization that answers a playbook run's module invocation with crafted ansible_facts.
- authentication_level: The attacker controls a managed host targeted by a run, so it can control the JSON its modules (or the setup module) return. That could be a compromised host, or an impostor accepted by trust-on-first-use for an unpinned host. No Pulsix account is needed.
- data_state: The run opens another SSH connection to that same host after the fact is returned, which is any multi-task play or any play with gather_facts. The single-task ad-hoc playbook (playbook.go:189-195, gather_facts false) offers no later connection.
- system_configuration: Default inject_facts_as_vars=True, which the worker does not change (ansible-core 2.21.4 in the worker image; the default is deprecated for removal in 2.24). With PlayUID set, the ProxyCommand runs as the play uid with group access to the run's work dir. In the embedded worker it runs as the manager uid, which was not examined here.

### Bounded local reproduction

Inputs:
- `{"changed": false, "ansible_facts": {"pulsix_ssh_common_args": "-o StrictHostKeyChecking=no -o ProxyCommand=\"touch /tmp/run/h12-marker\""}}`

Steps:
1. In the worker image (noixdev/pulsix-worker:dev, ansible-core 2.21.4) with no network, reproduce the worker's run layout with dummy data. Use an executable inventory script whose host 'victim' (127.0.0.1:2222) has pulsix_ssh_common_args='-o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-<uuid>'. Use an extra-vars file equal to forcedConnectionVars (ansible_ssh_common_args="{{ pulsix_ssh_common_args | default('') }}", ansible_ssh_args with -F ssh_config and the run's known_hosts). Use ansible.cfg as rendered by ansibleCfgText and the subprocess env from Runner.subprocessEnv.
2. Use a play with gather_facts false. T1 is ping over ssh (ignore_unreachable). T2 is a custom module that prints the payload JSON; it runs with connection: local to stand in for module output produced by the managed host's python, and is parsed and cleaned by the same action-plugin path. T3 debugs the resolved values. T4 is ping over ssh (ignore_unreachable). T5/T6 stat the marker locally.
3. Run ansible-playbook -vvvv -i inv.sh -e @extravars.json play.yml and compare the 'SSH: EXEC' lines of T1 and T4. Then check for /tmp/run/h12-marker.
4. Repeat with ANSIBLE_INJECT_FACT_VARS=False (control), and again with the carrier renamed to ansible_ssh_pulsix_common_args (fix check).

Observed: Audit artifacts of the validation run (agents/v08-ssh-pins/artifacts/evidence.txt, evidence2.txt, evidence3.txt; not repository sources) record the following. In the worker image, ansible-core 2.21.4 reports INJECT_FACTS_AS_VARS(default) = True with the run's ansible.cfg, and its clean_facts strips returned facts matching the magic variable names, the common connection vars, ^ansible_<connection>_, ansible_become_* and ^ansible_.*_interpreter$, so a pulsix_* fact is not matched. Run A (worker defaults): on T1 the ssh command line carried -o StrictHostKeyChecking=yes -o HostKeyAlias=pulsix-host-11111111-1111-1111-1111-111111111111. After T2 returned ansible_facts.pulsix_ssh_common_args, both pulsix_ssh_common_args and the forced ansible_ssh_common_args resolved to the module-supplied '-o StrictHostKeyChecking=no -o ProxyCommand="touch /tmp/run/h12-marker"'. On T4, ssh ran with -o StrictHostKeyChecking=no -o 'ProxyCommand=touch /tmp/run/h12-marker' and no HostKeyAlias, and the marker file was created on the controller (MARKER_EXISTS=True; the file listing shows it). Run B (ANSIBLE_INJECT_FACT_VARS=False): the pinned options stayed on the T4 ssh command line and MARKER_EXISTS=False. Run C (carrier renamed to ansible_ssh_pulsix_common_args, defaults otherwise): clean_facts stripped the returned fact, the pinned options stayed, and MARKER_EXISTS=False. Not exercised: module output over a real sshd from a remote host, and the ansible-runner wrapper (it sets only a jsonfile fact cache and nothing about fact injection).

Invariant proven: Pinned per-host SSH options (StrictHostKeyChecking=yes, HostKeyAlias=pulsix-host-<id>, the aliased ProxyJump chain) must hold for every connection of the run whatever a managed host returns. Only the worker, or by design a playbook author, may change them. A managed host must never be able to remove its own pin or inject ssh options that run commands on the worker.

### Remediation

Keep the per-host pinned options under a name that ansible-core strips from returned facts, and stop injecting facts as top-level vars. Rename pinnedCommonArgsVar to an ansible_ssh_-prefixed name such as ansible_ssh_pulsix_common_args, which clean_facts removes through its ^ansible_ssh_ rule (verified in run C). Add that name to ansiblevars' forbiddenNames and to the worker's extra-var guard. Also, as defense in depth, set inject_facts_as_vars = False in ansibleCfgText and ANSIBLE_INJECT_FACT_VARS=False in subprocessEnv. Playbooks that rely on top-level ansible_* facts would then need ansible_facts.*, and the ansible-core 2.24 default change will require that anyway. Fix the design comment at inventory.go:351-361 and add a test asserting the carrier name matches ^ansible_ssh_ and that ansible.cfg disables fact injection.

`internal/worker/runner/inventory.go`:

```go
// pinnedCommonArgsVar is the hostvar each host's SSH common args move into.
// It must start with "ansible_ssh_": ansible-core's clean_facts strips every
// returned fact matching ^ansible_ssh_, so a managed host cannot shadow it
// with a fact (inject_facts_as_vars ranks facts above inventory host vars).
const pinnedCommonArgsVar = "ansible_ssh_pulsix_common_args"
```

`internal/worker/runner/layout.go`:

```go
	return fmt.Sprintf(`[defaults]
host_key_checking = True
inject_facts_as_vars = False
retry_files_enabled = False
local_tmp = %s
%s
[ssh_connection]
ssh_args = %s
`, filepath.Join(workDir, localTmpName), vault, sshArgs(workDir))
```

`internal/worker/runner/runner.go`:

```go
		"ANSIBLE_HOST_KEY_CHECKING=True",
		"ANSIBLE_INJECT_FACT_VARS=False",
		"ANSIBLE_RETRY_FILES_ENABLED=False",
```

`internal/ansiblevars/ansiblevars.go`:

```go
	"ansible_ssh_common_args":        true,
	"ansible_ssh_pulsix_common_args": true, // carrier of the pinned per-host options
```

## Pending invitations outlive the inviting admin's removal, demotion or deactivation and cannot be revoked, so a removed admin can regain admin access

Fingerprint `tenancy/AcceptInvite:pending-invite-survives-issuer-removal-no-revocation` — severity **medium**

An org admin (members.manage) can create invitations of up to the admin role for any address, including one they control. On an instance without outbound mail the API returns the accept link to the inviter (member_handlers.go:154); with mail it goes to the inviter-chosen mailbox. Invitations stay valid for 7 days (InviteTTL). AcceptInvite and the PeekInvite/TakeInvite SQL check only the token hash, accepted_at and expiry. Nothing checks whether the issuer (invited_by) is still a member, still holds members.manage, or is still an active account. RemoveMember, ChangeRole (demotion) and DeactivateUser leave the issuer's pending invitations untouched. The API has no route to list or revoke pending invitations; the only invite DELETE is internal cleanup after a failed mail send. The SPA comment documents the missing list route. An admin who expects removal can mint admin invitations for alternate addresses in advance. After the owner removes or demotes them, or an instance admin deactivates them, they redeem a link anonymously and rejoin the organization as admin under a fresh account. The new admin account can mint further invitations before it is noticed. The owner therefore cannot fully evict a malicious admin through the product: invitation creation is audited, but outstanding invitations cannot be cancelled.

### Trace

1. **entrypoint** `internal/api/member_handlers.go:142` (POST /api/v1/orgs/{org}/invites): An admin passes requireOrg(PermMembersManage) and creates an invitation with role up to admin (owner is refused at service.go:332) for an address of their choosing.
2. **propagation** `internal/tenancy/service.go:375` (Service.Invite): When the mailer returns mail.ErrNotConfigured, the plaintext accept link is set on the result, and the handler returns it as accept_url (member_handlers.go:154). With mail configured it goes to the inviter-chosen address. The row stores invited_by (service.go:347), which no later code reads.
3. **propagation** `internal/tenancy/service.go:542` (Service.RemoveMember / ChangeRole (513) / DeactivateUser (571)): The owner removes or demotes the admin, or an instance admin deactivates them. Only the membership row is deleted or updated, or the user is disabled and their sessions and tokens revoked. Pending invites from this issuer stay valid.
4. **propagation** `internal/api/member_handlers.go:233` (POST /api/v1/invites/accept): Public route with no principal. The removed admin redeems the link anonymously with a new name and password. For an address with no account, checkAcceptor (service.go:469-482) requires only a name and an 8-character password.
5. **propagation** `internal/db/queries/tenancy_users.sql:80` (TakeInvite): Accepts any unaccepted, unexpired token hash. It has no condition on invited_by, org_memberships or users.disabled_at.
6. **sink** `internal/tenancy/service.go:459` (Service.AcceptInvite AddMembership): After creating the new account (service.go:437-441), adds the membership with inv.Role (admin) to the organization that removed the issuer.

### Evidence

- `internal/db/queries/tenancy_users.sql:80`: TakeInvite: UPDATE invites SET accepted_at ... WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now RETURNING *. PeekInvite (line 69) has the same filter. Neither has an issuer-authority condition.
- `internal/db/migrations/00007_invites.sql:9`: invited_by uuid NOT NULL REFERENCES users(id) is stored. No trigger or query consults it at acceptance, on membership removal, or on demotion.
- `internal/tenancy/service.go:542`: RemoveMember calls only GetMembership and DeleteMembership. ChangeRole (513) calls only UpdateMembershipRole. DeactivateUser (571) only disables the user and revokes sessions and API tokens. None touches invites.
- `internal/tenancy/service.go:459`: AcceptInvite grants inv.Role from the token without re-validating the issuer.
- `internal/db/queries/tenancy_users.sql:117`: The only invite DELETE is used by Service.Invite (service.go:377) to remove an invitation whose mail delivery failed. No route or service method revokes a pending invitation.
- `web/src/features/members/MembersPage.tsx:26`: States that the API has no GET route listing pending invitations (only POST /invites and POST /invites/accept), so the Invitations tab is omitted. An owner cannot see or revoke them.
- `internal/api/member_handlers.go:154`: Returns inv.AcceptURL to the inviter. On an instance without outbound mail, that is the redeemable link.

### Principal, resource and conditions

- Attacker: A malicious or about-to-be-removed organization admin who wants to keep admin access after the owner removes or demotes them, or after an instance admin deactivates their account.
- authorization_role: Before losing authority, the attacker held the admin role (members.manage) in the organization and created one or more invitations.
- timing_dependency: The attacker redeems the invitation within InviteTTL (7 days) of creating it, after being removed, demoted or deactivated.
- data_state: The attacker controls the invitation link: either the instance has no outbound mail (the link is returned in the API response), or the invited address is a mailbox the attacker controls.

### Bounded local reproduction

Inputs:
- `svc.Invite(ctx, org.ID, admin, "admin-alt@example.test", tenancy.RoleAdmin) with a mailer returning mail.ErrNotConfigured; then either svc.RemoveMember(ctx, org.ID, RoleOwner, owner, admin) + svc.DeactivateUser(ctx, true, admin), or svc.ChangeRole(ctx, org.ID, RoleOwner, owner, admin, RoleViewer); then svc.AcceptInvite(ctx, uuid.Nil, inv.Token, "Alt", "long-enough-password")`

Steps:
1. Copied the repository to the verifier scratch directory. Added internal/tenancy/v12_invite_revoke_test.go (TestV12InviteOutlivesIssuerAuthority), which uses the package's existing in-memory fakeStore and fakeMailer with dummy principals. The fake's PeekInvite and TakeInvite mirror the real SQL filters. In the scratch copy only, store_sql_test.go was moved aside because it imports the testcontainers-backed testdb.
2. Ran `go test -count=1 -v -run 'TestV12InviteOutlivesIssuerAuthority|TestAcceptInvite' ./internal/tenancy/` in the approved Docker sandbox: no network, read-only root, source and module cache, uid 65532, cap-drop ALL, 2 CPU, 2 GiB, 512 pids, 300 s timeout.
3. Subtest removed_and_deactivated: the owner creates an org, a dummy admin is added, and the admin invites admin-alt@example.test as admin and receives the link. The owner removes the admin and an instance admin deactivates them. The link is then redeemed anonymously. Subtest demoted_to_viewer: same setup, but the owner demotes the admin to viewer before redemption.
4. Source check for the real store: TakeInvite and PeekInvite SQL, the RemoveMember, ChangeRole and DeactivateUser service paths, and the API route set contain no issuer check and no invite revocation.

Observed: removed_and_deactivated: issuer membership_err=not a member, disabled=true, pending invites=1. RESULT accept_err=<nil>, org_match=true, new_account=true, new_member_role=admin. demoted_to_viewer: issuer role=viewer, pending invites=1. RESULT accept_err=<nil>, org_match=true, new_account=true, new_member_role=admin. The existing TestAcceptInvite* tests passed (agents/v12-invite/artifacts/evidence.txt).

Invariant proven: Removing, demoting or deactivating a member should end every authority that member delegated through pending invitations. At acceptance the issuer must still be an active member whose role holds members.manage, or the issuer's pending invites must be deleted as soon as they lose that authority. Org admins and owners should be able to list and revoke pending invitations.

### Remediation

Bind invite validity to the issuer's live authority, and add revocation. (1) In PeekInvite and TakeInvite, require the issuer to still be an active member of the invite's org with a role that holds members.manage (owner or admin). The accept path runs without pulsix.org_id, so the RLS bypass policy lets the EXISTS subquery see org_memberships. (2) Delete the issuer's pending invites for that org in RemoveMember and in ChangeRole when the new role drops below admin. Delete all of a user's pending invites in DeactivateUser. Do this in the same transaction as the membership change where possible. (3) Add admin-only GET /orgs/{org}/invites and DELETE /orgs/{org}/invites/{id} routes, audited, so owners can see and revoke pending invitations, and surface them in the members page. Regression tests: an invite whose issuer was removed, demoted below admin, or deactivated before acceptance returns ErrNotFound and adds no membership; a revoked invite returns ErrNotFound.

`internal/db/queries/tenancy_users.sql`:

```go
-- name: TakeInvite :one
UPDATE invites AS i SET accepted_at = sqlc.arg(now)::timestamptz
WHERE i.token_hash = $1 AND i.accepted_at IS NULL AND i.expires_at > sqlc.arg(now)::timestamptz
  AND EXISTS (SELECT 1 FROM org_memberships m JOIN users u ON u.id = m.user_id
              WHERE m.org_id = i.org_id AND m.user_id = i.invited_by
                AND m.role IN ('owner','admin') AND u.disabled_at IS NULL)
RETURNING i.*;

-- (apply the same EXISTS condition to PeekInvite)

-- name: DeletePendingInvitesByIssuerInOrg :exec
DELETE FROM invites WHERE org_id = $1 AND invited_by = $2 AND accepted_at IS NULL;

-- name: DeletePendingInvitesByIssuer :exec
DELETE FROM invites WHERE invited_by = $1 AND accepted_at IS NULL;
```

`internal/tenancy/service.go`:

```go
// RemoveMember, after DeleteMembership succeeds:
if err := s.store.DeletePendingInvitesByIssuerInOrg(ctx, orgID, target); err != nil {
	return fmt.Errorf("revoke pending invites: %w", err)
}
// ChangeRole, after UpdateMembershipRole succeeds:
if !Allows(role, PermMembersManage) {
	if err := s.store.DeletePendingInvitesByIssuerInOrg(ctx, orgID, target); err != nil {
		return fmt.Errorf("revoke pending invites: %w", err)
	}
}
// DeactivateUser, after RevokeTokensForUser:
if err := s.store.DeletePendingInvitesByIssuer(ctx, target); err != nil {
	return fmt.Errorf("revoke pending invites: %w", err)
}
```

