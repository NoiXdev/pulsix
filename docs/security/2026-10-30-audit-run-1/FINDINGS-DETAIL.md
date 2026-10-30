# Findings detail — confirmed medium, high and critical

## Agent-reported host facts are rendered as trusted Ansible hostvars, so a monitored host can run commands on the org's worker controller

Fingerprint `internal/inventory/render/render.go:hostVars:pulsix_facts-trusted-template` — severity **high**

A host agent's Facts push stores arbitrary strings (up to 512 bytes, up to 64 keys) in hosts.facts. Every job's inventory snapshot then renders them verbatim as _meta.hostvars.<host>.pulsix_facts with no unsafe marking. The worker writes that document unchanged and passes an executable exec-cat wrapper as --inventory, so ansible-core 2.21.4 (the worker image pin) loads it through the script inventory plugin, which treats hostvars strings as trusted templates. When a play evaluates pulsix_facts (the documented purpose of the variable), a Jinja expression or lookup planted in a fact is evaluated in the Ansible controller process on the zone worker. Whoever controls one monitored host's agent identity (root on that host, or its agent certificate and key) can therefore execute commands as the run's play identity on the worker, with access to the material that run is given for every host in its scope. The webhook extra-vars path already neutralises exactly this with ansiblevars.Inert; the agent-facts path does not.

### Trace

1. **entrypoint** `internal/agentapi/stream.go:145` (Service.process (AgentStream Facts)): The mTLS-authenticated agent's Facts message values are forwarded to the FactsSink, bound to the agent's own org and host from the interceptor-resolved identity.
2. **propagation** `internal/agents/facts.go:82` (Service.RecordFacts): After checking only key count, non-empty keys and value byte length (line 75), values containing Jinja delimiters are passed to the store.
3. **propagation** `internal/agents/store_sql.go:265` (SQLStore.RecordFacts): Values are JSON-encoded (line 261) and merged verbatim into hosts.facts via UpsertHostFacts (jsonb ||, internal/db/queries/agents.sql line 111).
4. **propagation** `internal/inventory/renderer.go:182` (Renderer (inventory snapshot)): The stored host facts are passed into render.Host.Facts for the job's inventory snapshot.
5. **propagation** `internal/inventory/render/render.go:390` (renderer.hostVars): Facts are emitted as _meta.hostvars.<host>.pulsix_facts with no __ansible_unsafe wrapping.
6. **propagation** `internal/worker/runner/inventory.go:208` (Runner.materializeInventory): mergeInventory only adds connection variables; the document is written unchanged as the inventory document, and the exec-cat wrapper (layout.go line 136) is passed as --inventory (runner.go line 193).
7. **sink** `deploy/worker/Dockerfile:69` (ansible-core 2.21.4 script inventory plugin + templating): The pinned ansible-core treats script-inventory hostvars strings as trusted templates; evaluating pulsix_facts renders the planted expression, including lookup('pipe', ...), in the controller process.

### Evidence

- `internal/inventory/render/render.go:390`: vars["pulsix_facts"] = h.Facts with no ansiblevars.Inert wrapping.
- `internal/agents/facts.go:75`: The only value check is a byte-length bound; content is unrestricted.
- `internal/worker/runner/layout.go:136`: The inventory reaches Ansible through an exec-cat script, i.e. the script inventory plugin.
- `internal/ansiblevars/untrusted.go:27`: Inert exists and is applied to webhook extra vars (internal/jobs/run_overrides.go line 75), but not to facts.
- `deploy/worker/Dockerfile:69`: The worker pins ansible-core 2.21.4; in that image a full ansible-playbook run against the rendered document evaluated the fact's Jinja and pipe lookup, while the __ansible_unsafe-wrapped control stayed literal.

### Principal, resource and conditions

- Attacker: Root on one host monitored by the Pulsix agent, sending a Facts message over that agent's own mTLS stream.
- authentication_level: The attacker holds a valid agent identity for one monitored host: root on that host, or its agent certificate and key.
- data_state: The host is managed (rendered into job inventories) and a play run against it evaluates pulsix_facts, directly or through hostvars.
- user_interaction: An operator, schedule or trigger runs such a play after the malicious facts push.

### Bounded local reproduction

Inputs:
- `Facts{values: {"os": "debian-{{ 6 * 7 }}-uid{{ lookup('pipe', 'id -u') }}"}}`
- `Rendered inventory: {"_meta":{"hostvars":{"web-1":{"ansible_host":"127.0.0.1","pulsix_facts":{"os":"debian-{{ 6 * 7 }}-uid{{ lookup('pipe', 'id -u') }}"}}}},"all":{"children":["ungrouped"]},"ungrouped":{"hosts":["web-1"]}}`
- `Playbook task: ansible.builtin.debug: msg="host os is {{ pulsix_facts.os }}" (hosts: web-1, gather_facts: false)`

Steps:
1. In a scratch copy of the repo, a test in internal/inventory/render calls the real agents.Service.RecordFacts (fake store) with the payload fact, then render.Render for a managed host carrying the stored fact, and prints the document (golang:1.27.1-alpine, --network none, read-only, cap-drop ALL, user 65532, prescribed limits).
2. The printed document is served by a two-line executable inventory script (#!/bin/sh; exec cat <doc>), mirroring inventoryWrapperText; a control document wraps the same value as {"__ansible_unsafe": ...}.
3. In noixdev/pulsix-worker:dev (ansible-core 2.21.4), with --network none --read-only, the prescribed limits and --ipc private --shm-size 64m, run ansible-playbook -i <script> play.yml for both inventories.

Observed: RecordFacts accepted the value (err=nil) and Render emitted it raw under pulsix_facts.os. The full ansible-playbook run printed "host os is debian-42-uid65532": the arithmetic evaluated and the pipe lookup ran `id -u` in the controller process. With the __ansible_unsafe-wrapped control the same task printed the value literally.

Invariant proven: Data reported by a host agent is lower-trust than the controller and must reach Ansible as inert data: every string under pulsix_facts should be wrapped as {"__ansible_unsafe": ...} (ansiblevars.Inert), as webhook variables already are, so it renders literally and is never evaluated.

### Remediation

Mark every agent-reported fact unsafe before it enters the inventory document by reusing ansiblevars.Inert (h.Facts is map[string]any, which Inert handles recursively). Add a regression test asserting that pulsix_facts strings render as {"__ansible_unsafe": ...}. Optionally also refuse '{{', '{%' and '{#' in RecordFacts as defence in depth.

`internal/inventory/render/render.go`:

```go
	if len(h.Facts) > 0 {
		// Facts are reported by the host's agent: host-controlled data
		// must never be templated on the worker's controller.
		vars["pulsix_facts"] = ansiblevars.Inert(h.Facts)
	}
```

## Auth rate limit checks before the handler and charges after it, so concurrent TOTP guesses from one address get past the failed-attempt budget

Fingerprint `internal/api/auth_rate_limit.go:authRateLimitMiddleware:check-then-charge-concurrency-bypass` — severity **medium**

authRateLimitMiddleware first asks the limiter whether the address has already used up its budget. It then runs the handler and charges a failure only after the handler returns. Nothing reserves a slot before the handler runs. So every request that arrives before enough failures have been charged gets past the gate. A caller who has the victim's password, and so a pending session, can send large concurrent bursts of POST /api/v1/auth/totp/verify in each one-minute window. The configured limit is 20 failed guesses per minute (PULSIX_AUTH_RATE_LIMIT default), but a burst is limited only by how many requests the server accepts at once. The source has no per-account TOTP failure counter or lockout, so this per-address budget is the only limit on guessing the 6-digit second factor. Each guess matches one of 3 accepted time steps (skew 1). The same middleware also guards login, passkey login finish and invite accept.

### Trace

1. **entrypoint** `internal/api/mfa_handlers.go:148` (POST /api/v1/auth/totp/verify): A pending session (password already proven) submits a TOTP or recovery code. The route is registered with withAuthRateLimit(deps) at line 160.
2. **propagation** `internal/api/auth_rate_limit.go:75` (authRateLimitMiddleware): lim.Refused(ip) only reads the counter. It refuses only when the charged count is already at the limit.
3. **propagation** `internal/clientip/limiter.go:77` (Limiter.Refused): Checks the count under the mutex and changes nothing, so no slot is reserved for this attempt.
4. **propagation** `internal/api/auth_rate_limit.go:79` (authRateLimitMiddleware): The handler (verifyFactorCode, then TOTP.Verify) runs for every request that passed the check.
5. **sink** `internal/api/auth_rate_limit.go:82` (authRateLimitMiddleware): A failure is charged only after the handler returns. By then every concurrent attempt has already been checked against the secret.

### Evidence

- `internal/api/auth_rate_limit.go:79`: next(ctx) sits between the Refused check (line 75) and the Charge (line 82).
- `internal/clientip/limiter.go:94`: Charge caps the count at the limit and runs in its own locked section, separate from Refused.
- `internal/auth/totp.go:285`: verifyAndConsume compares the code against 3 steps (skew 1). A miss returns false at lines 289-291 and consumes nothing. No per-account failure counter exists in internal/auth or internal/api.
- `internal/config/config.go:121`: DefaultAuthRateLimit = 20 failed attempts per minute per address, per endpoint, is the only bound the code intends.

### Principal, resource and conditions

- Attacker: A client with a pending session for an account protected by TOTP sends a burst of concurrent wrong-code verify requests from one address.
- authentication_level: The attacker has the victim's password, or some other pending session that has not completed MFA, which is needed to reach /auth/totp/verify. Login brute force is also affected, but each attempt there costs an argon2id hash.
- system_configuration: PULSIX_AUTH_RATE_LIMIT > 0 (default 20). The limiter is in-process, one per manager.
- timing_dependency: The requests must be in flight at the same time, before enough failures have been charged. Burst size depends only on the attacker's connections and on how many requests the server accepts and queues at once.

### Bounded local reproduction

Inputs:
- `internal/api/v04_ratelimit_test.go (scratch copy only): a huma POST /verify route on chi/humachi behind the production withAuthRateLimit(Deps{AuthRateLimit: 20, ClientIP: clientip.New(nil)}). The handler sleeps 5 ms and returns 401, standing in for a wrong TOTP code. The route is served over loopback with httptest.NewServer. The test sends 50 sequential requests to one server, then 200 concurrent requests (released together, no barrier in the handler) to a fresh server, then one follow-up request.`

Steps:
1. Copy the repository into scratch and delete internal/api/*_test.go in the copy, because those tests import the database harness. Add the harness test there.
2. Run go test -count=1 -v -run TestV04AuthRateLimitConcurrent ./internal/api/ in the approved Docker sandbox: golang:1.27.1-alpine, --network none, read-only source and module cache, env -i allowlist, cap-drop ALL, uid 65532, 300 s timeout. Write stdout to evidence.txt.

Observed: Sequential: limit=20, 50 attempts, 20 reached the handler (20x 401, 30x 429). Concurrent: limit=20, 200 attempts from one address, 198 reached the handler (198x 401, 2x 429). After the burst the next request got 429. Source: agents/v04-session-auth/artifacts/evidence.txt. This independently reproduces the hunter's result (40 of 40 against a limit of 3, with a barrier), here with the default limit, real HTTP and no barrier.

Invariant proven: At most PULSIX_AUTH_RATE_LIMIT failed attempts per address per minute should reach the TOTP verify, login, passkey-login and invite-accept handlers, however many requests are in flight at once. That is the limit documented at internal/config/config.go:68-77 and in the middleware's doc comment (internal/api/auth_rate_limit.go:46-62).

### Remediation

Reserve the attempt atomically before the handler runs: claim the slot under the limiter mutex and refund it on 2xx, 5xx or 429. Also add a per-account counter or lockout for failed second-factor attempts on /auth/totp/verify that does not depend on the client address.

`internal/clientip/limiter.go`:

```go
// Reserve atomically claims one attempt for a; ok=false means the budget is spent.
func (l *Limiter) Reserve(a netip.Addr) (remaining time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	k := key(a)
	w, found := l.seen[k]
	if found && now.Sub(w.start) < l.window {
		if w.count >= l.limit {
			return l.window - now.Sub(w.start), false
		}
		w.count++
		return 0, true
	}
	if !found && len(l.seen) >= l.max {
		l.makeRoom(now)
	}
	l.seen[k] = &window{start: now, count: 1}
	return 0, true
}

// Refund returns a reserved attempt that did not fail.
func (l *Limiter) Refund(a netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.seen[key(a)]; ok && w.count > 0 {
		w.count--
	}
}
```

`internal/api/auth_rate_limit.go`:

```go
ip := clientAddr(deps, ctx)
if remaining, ok := lim.Reserve(ip); !ok {
	writeRateLimited(ctx, remaining)
	return
}
next(ctx)
if s := ctx.Status(); s < http.StatusBadRequest || s >= http.StatusInternalServerError || s == http.StatusTooManyRequests {
	lim.Refund(ip)
}
```

## Run-output redaction misses secrets that contain a quote or backslash together with &, < or > in ansible's display text

Fingerprint `internal/worker/runner/redact.go:newRedactor:go-html-json-escape-vs-python-json` — severity **medium**

The worker's redactor replaces each claim secret's raw value and its Go json.Marshal spelling. ansible-core's result display (default callback, result_format json) serializes module results with Python json.dumps(ensure_ascii=False), which escapes " and \ but leaves &, < and > literal. Go's encoder HTML-escapes those three (\u0026, \u003c, \u003e). So for a secret that has a JSON-escaped character and one of &, < or >, neither listed spelling appears in the display stdout. The secret is forwarded unredacted in the event's stdout field, stored as-is in job_events, and shown to any org member with jobs.read (viewer) through the events API, the SSE stream and the run view's EventLog. The structured event_data.res copy of the same value is redacted, so the gap is only the display text. Evidence from the audit run: a real runner_on_ok event captured from the worker image (ansible-runner 2.4.3 / ansible-core 2.21.4, network none) carries stdout `"db_password": "Tr&ub4dor\"3x"`, so & is literal and " is escaped; feeding that exact line through the real Runner.streamEvents with Redact=[Tr&ub4dor"3x, Troub4dor"3x] forwarded stdout that still contained the secret, while the control secret without & was redacted and res.db_password was [redacted] in both.

### Trace

1. **entrypoint** `internal/jobs/queue_sql.go:645` (SQLQueue.buildPayload (job claim)): On claim, every opened credential (and, on the next line, inventory secrets) goes onto workerpb.Job.redact. This is where the protected secret enters the run. The lower-trust reader is the viewer at the sink.
2. **propagation** `internal/worker/runner/redact.go:54` (newRedactor.add): The only escaped spelling is built with json.Marshal, which HTML-escapes & < >. The set holds the raw value, the Go-escaped value and base64 forms, but not the Python json.dumps form.
3. **propagation** `internal/worker/runner/runner.go:446` (Runner.streamEvents): Each ansible-runner --json event line with a state is forwarded as red.eventPayload(ev).
4. **propagation** `internal/worker/runner/redact.go:179` (redactor.eventPayload): The display stdout (ansible's json.dumps spelling of the result) only goes through text(), which replaces the listed literals. None of them match.
5. **propagation** `internal/jobs/events.go:445` (SQLQueue.appendEvent): The manager stores the payload as received, because normalizePayload passes valid JSON through unchanged, and publishes it to SSE subscribers.
6. **sink** `internal/api/job_handlers.go:490` (GET /api/v1/orgs/{org}/jobs/{id}/events): requireOrg(PermJobsRead) lets a viewer read the stored payload, including the unredacted stdout. The SSE route (job_sse.go:40) has the same gate.

### Evidence

- `internal/worker/runner/redact.go:55`: esc := string(b[1 : len(b)-1]) from json.Marshal is the only escaped spelling added.
- `internal/worker/runner/redact.go:133`: text() does literal strings.ReplaceAll on the listed values only.
- `internal/tenancy/roles.go:113`: PermJobsRead is granted to RoleViewer.
- `web/src/features/runs/EventLog.tsx:62`: The run view renders payload.stdout verbatim.

### Principal, resource and conditions

- Attacker: An org viewer with jobs.read opens the run log, or calls the events/SSE API, for a job whose task printed a secret containing such characters.
- authorization_role: The reader needs only the viewer role (jobs.read) in the organization that ran the job.
- data_state: The secret has at least one JSON-escaped character (", \ or a control character, e.g. a newline in a multi-line value) AND at least one of &, < or >.
- user_interaction: A task's display output includes the value, e.g. debug of the var, a module result echoing it, or a failed command's output in the JSON result. This is the accidental print that redaction exists to catch.
- system_configuration: The default ansible result_format json applies. The worker-generated ansible.cfg (layout.go ansibleCfgText) does not change it.

### Bounded local reproduction

Inputs:
- `secret on the claim redact list: Tr&ub4dor"3x (control: Troub4dor"3x)`
- `playbook task: ansible.builtin.debug: var=db_password with extravars {"db_password": "Tr&ub4dor\"3x"}`

Steps:
1. In the worker image noixdev/pulsix-worker:dev (network none, read-only, tmpfs-only writes), run ansible-runner run on a local-connection playbook with two debug tasks printing db_password=Tr&ub4dor"3x and ctrl_password=Troub4dor"3x. Capture the runner_on_ok job_event lines.
2. Copy the repository into scratch and add a package-internal test in internal/worker/runner. It feeds those exact lines to (&Runner{}).streamEvents with newRedactor(&workerpb.Job{Redact: []string{`Tr&ub4dor"3x`, `Troub4dor"3x`}}), then decodes each forwarded payload's stdout.
3. Run go test -run TestV17RealAnsibleDisplayRedaction ./internal/worker/runner/ in the network-less, read-only golang sandbox.

Observed: ansible's display stdout was ok: [localhost] => {\r\n    "db_password": "Tr&ub4dor\"3x"\r\n}. After streamEvents, the forwarded payload's stdout still read "db_password": "Tr&ub4dor\"3x" (contains secret=true), while event_data.res.db_password was [redacted]. The control secret Troub4dor"3x was [redacted] in both stdout and res. Storage and API exposure are source-verified only, because the DB-backed packages cannot run in the sandbox. Recorded in the v17-job-output run artifacts: evidence2.txt line 4 is the real ansible-runner 2.4.3 / ansible-core 2.21.4 runner_on_ok event, and evidence.txt lines 6 and 11 are the redaction outcome for the control secret and the & secret.

Invariant proven: internal/jobs/redact.go (package comment) says viewers with jobs.read must never see a credential, secret extra var or secret inventory var that a task printed. internal/worker/runner/redact.go:43-44 says each value is also redacted in its JSON-escaped spelling because display text shows module results as JSON. That spelling has to match what ansible actually emits.

### Remediation

Add the Python json.dumps(ensure_ascii=False) spelling next to the Go one. Escape only ", \ and control characters, and leave &, <, >, U+2028/U+2029 and non-ASCII literal. A plain SetEscapeHTML(false) encoder still differs on U+2028/U+2029 and invalid UTF-8, so a hand-written escaper is the exact match. Keep the existing json.Marshal spelling. Add a regression test that feeds a recorded ansible-runner event with a secret mixing "/\ with &/</>.

`internal/worker/runner/redact.go`:

```go
// Add "fmt" to the imports of internal/worker/runner/redact.go.

// jsonSpellings returns every JSON string-body spelling of v a run can print:
// Go's HTML-escaping json.Marshal form and the Python json.dumps(ensure_ascii=False)
// form ansible's result display uses (escapes only '"', '\\' and control characters).
func jsonSpellings(v string) []string {
	var out []string
	if b, err := json.Marshal(v); err == nil {
		out = append(out, string(b[1:len(b)-1]))
	}
	var sb strings.Builder
	for _, r := range v {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	out = append(out, sb.String())
	return out
}

// in newRedactor.add, replacing the single json.Marshal spelling:
for _, esc := range jsonSpellings(v) {
	if esc != v {
		set[esc] = true
	}
}
```

## An org owner on an instance without mail can pre-create the account for someone else's email and capture that person's later invitations from other organizations

Fingerprint `tenancy/AcceptInvite:unmailed-invite-link-binds-email-identity-without-mailbox-proof` — severity **medium**

When the instance has no outbound mail settings, POST /api/v1/orgs/{org}/invites returns the single-use accept_url to the inviting administrator instead of mailing it. POST /api/v1/invites/accept treats the token as proof that the redeemer controls the invited address. On first redemption it creates a user bound to that email with the redeemer's chosen password and opens a session in which the redeemer enrols their own second factor. Any MFA-complete user can create an organization (POST /api/v1/orgs), become its owner, and invite any address. That user can redeem their own link and end up owning a working account for a victim's email that the victim never touched. AcceptInvite later binds every invitation for that address to the existing account on the token alone. If an unrelated organization then invites the real person and she accepts, the membership (up to admin) goes to the attacker-controlled user ID. She gets signed_in=false and cannot sign in, because the password and factor belong to the attacker, who can sign in and holds the invited role in the foreign tenant. The same root cause has a weaker side effect: an inviter holding an accept_url can attach an already-existing account to the inviter's org without its owner's consent.

### Trace

1. **entrypoint** `internal/api/org_handlers.go:71` (POST /api/v1/orgs): Any MFA-complete session user (requireMFAComplete, line 72) creates an organization. CreateOrg (tenancy/service.go:244) makes the caller its owner, which grants members.manage.
2. **propagation** `internal/api/member_handlers.go:146` (POST /api/v1/orgs/{org}/invites): After passing requireOrg(PermMembersManage), the attacker invites victim@corp.example to their own org.
3. **propagation** `internal/tenancy/service.go:364` (Service.Invite): mail.SMTP.Send (wired at app.go:233) returns an error wrapping mail.ErrNotConfigured when setup.LoadSMTP finds no settings (setup/service.go:328). The invitation is kept and inv.AcceptURL is set to the link.
4. **propagation** `internal/api/member_handlers.go:153` (invite create response): accept_url, which carries the single-use token, is returned to the inviter.
5. **propagation** `internal/api/member_handlers.go:223` (POST /api/v1/invites/accept): The attacker redeems the token with a password of their choice. AcceptInvite creates the user (service.go:421-427, newAccount=true), and line 241 opens a non-MFA session in which the attacker enrols their own TOTP or passkey through requireEnrolmentSession (mfa_handlers.go:92).
6. **propagation** `internal/tenancy/service.go:418` (Service.AcceptInvite (later invite from org B)): The real person redeems org B's invite. GetUserByEmail returns the attacker-created row, which has a password, so the default branch (lines 429-435) leaves its password and display name unchanged (newAccount=false).
7. **sink** `internal/tenancy/service.go:437` (Service.AcceptInvite): AddMembership(inv.OrgID, u.ID, inv.Role) gives org B's invited role (up to admin) to the attacker-controlled user. The attacker then signs in with their own password and second factor.

### Evidence

- `internal/tenancy/service.go:378`: The doc comment states the security assumption: 'The invite token only proves control of that mailbox'. The ErrNotConfigured branch breaks it, because the token goes to the inviter.
- `internal/tenancy/service.go:363`: case errors.Is(sendErr, mail.ErrNotConfigured): inv.AcceptURL = link. The token goes to the inviter, not to the mailbox.
- `internal/mail/smtp.go:81`: The production mailer's Send (built at app.go:233 from setup.LoadSMTP) wraps ErrNotConfigured whenever no SMTP settings are stored. The setup wizard explicitly treats SMTP as optional (setup/service.go:75-77).
- `internal/tenancy/service.go:429`: An existing account that has a password is reused silently. AddMembership at line 437 runs without any proof from the account holder.
- `internal/api/org_handlers.go:72`: Org creation is gated only by requireMFAComplete, so any interactive user of the instance can obtain members.manage in an org they own.
- `internal/db/queries/tenancy.sql:23`: AddMembership inserts with ON CONFLICT DO NOTHING. The membership goes to whichever user row owns the citext-unique email.

### Principal, resource and conditions

- Attacker: An MFA-complete user who owns an org on an instance without SMTP and targets a person likely to be invited later by a different tenant.
- authentication_level: The attacker is any MFA-complete session user of the instance (able to create an org through POST /api/v1/orgs), or an admin or owner of any org.
- system_configuration: The instance has no stored SMTP settings when the attacker creates the invite. This is the supported 'invite without mail' mode and the state after a setup that skipped mail. Mail may be configured later; the pre-created account persists.
- data_state: The victim's email address has no account on the instance yet.
- user_interaction: Another organization later invites that email address and the real person accepts the invitation.

### Bounded local reproduction

Inputs:
- `Independent harness internal/v13harness/v13_test.go (in a scratch copy of the source at b13f3c6): the real tenancy.NewService with an in-memory Store that follows the SQL contract (citext-unique email, atomic single-use TakeInvite, AddMembership ON CONFLICT DO NOTHING), and a Mailer whose Send returns fmt.Errorf("load smtp settings: %w", fmt.Errorf("not found: %w", mail.ErrNotConfigured)), the same shape as SMTP.Send plus setup.LoadSMTP`
- `svc.CreateOrg(attacker,...) -> orgA; svc.Invite(ctx, orgA, attacker, "Victim@Corp.example", viewer) -> token taken from AcceptURL; svc.AcceptInvite(ctx, tokA, "Victim", "attacker-pw-123456")`
- `svc.Invite(ctx, orgB, ownerB, "victim@corp.example", admin); svc.AcceptInvite(ctx, tokB, "Real Victim", "victim-pw-654321"); svc.MembershipOf(ctx, orgB, attackerCreatedUserID); auth.VerifyPassword on the stored hash`

Steps:
1. Copied the repository into the verifier scratch directory and added a separate package test that imports only internal/tenancy, internal/auth and internal/mail, with no database and no testdb.
2. Ran go test -count=1 -v -run TestV13UnmailedInvitePreHijack ./internal/v13harness/ in golang:1.27.1-alpine with --network none, --read-only, --cap-drop ALL, no-new-privileges, user 65532, 2 CPU / 2 GiB / 512 pids, env -i, GOPROXY=off, a read-only source tree and module cache, and a 300 s timeout.
3. Checked that org B's admin membership belongs to the user ID created by the attacker's redemption, and checked which password the stored hash accepts.

Observed: step1 orgA invite: mailed=false accept_url_present=true; step2 attacker redeems own link: newAccount=true (the handler opens a session); step3 victim redeems orgB invite: same user ID, newAccount=false; RESULT same_user=true orgB_role_of_attacker_created_user="admin" hash_accepts_attacker_pw=true hash_accepts_victim_pw=false display_name="Victim"; --- PASS (agents/v13-invite-setup/artifacts/evidence.txt)

Invariant proven: An account should be bound to an email address only by someone who proved control of that mailbox, or it should be marked unverified. Adding a membership to an existing account should require that account's holder to be authenticated, for example with an MFA-complete session whose user ID equals the invited account. An invite token that may have passed through a third party's hands should never be enough.

### Remediation

Never treat an account created from a link that was not mailed as an email-verified identity, and never attach a membership to an existing account on the token alone. In AcceptInvite, when the address already belongs to an account with a password, require the request to carry an MFA-complete interactive session whose user ID equals that account; otherwise refuse, without consuming the invite. Optionally, record email verification on users (set only when the link was actually mailed and redeemed) and refuse to reuse an unverified account for another org's invite. Add a regression test: an un-mailed invite redeemed by the inviter, followed by a second org's invite redeemed anonymously, must not add a membership to the first account.

`internal/tenancy/service.go`:

```go
// AcceptInvite gains the authenticated acceptor (uuid.Nil when none).
func (s *Service) AcceptInvite(ctx context.Context, acceptor uuid.UUID, token, displayName, password string) (uuid.UUID, uuid.UUID, bool, error) {
	sum := sha256.Sum256([]byte(token))
	inv, err := s.store.PeekInvite(ctx, sum[:], s.now()) // read without consuming
	if err != nil { /* map as today */ }
	u, err := s.store.GetUserByEmail(ctx, inv.Email)
	if err == nil && u.PasswordHash != nil && acceptor != u.ID {
		// An existing account joins only when its own holder is signed in.
		return uuid.Nil, uuid.Nil, false, ErrSignInRequired
	}
	inv, err = s.store.TakeInvite(ctx, sum[:], s.now()) // atomic consume, as today
	// ... create-or-reuse and AddMembership unchanged ...
}
```

`internal/api/member_handlers.go`:

```go
var acceptor uuid.UUID
if p, ok := auth.FromContext(ctx); ok && p.MFAComplete && p.TokenID == nil {
	acceptor = p.UserID
}
uid, orgID, newAccount, err := deps.Tenancy.AcceptInvite(ctx, acceptor, in.Body.Token, in.Body.DisplayName, in.Body.Password)
```

