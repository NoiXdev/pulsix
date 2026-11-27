# Agent status page (backlog 3.132)

Status: draft for owner approval, 2026-11-27.

## Intent

The agent runs on machines that often belong to someone else, such as a
customer's server. People on that machine should be able to see what the agent
does without access to the Pulsix manager. Two kinds of people want this:

- **Support.** When something is wrong, they need to know whether the agent is
  connected, since when, which checks run, and what is waiting in the buffer.
- **The curious.** A customer's IT team, or anyone who asks "what does this
  thing actually collect and send?", should be able to look and see it.

The page is about **transparency**. It shows what the agent collects, exactly as
it sends it, and what else the agent is allowed to do on the machine. It is not
a customer-facing service status page; that is v0.5 (5.1–5.9).

## Goals

- The agent can serve a read-only HTML page and the same data as JSON
  (`/status.json`).
- The page shows:
  - **Agent:** version, operating system and architecture, start time.
  - **Connection:** the manager address it talks to; whether it is connected,
    since when, last contact, and last heartbeat.
  - **Identity:** the local host name, the organization and host as Pulsix
    names them, when the certificate expires, and when the next renewal is due.
    *My call:* the agent does not know the organization or host name today
    (enrolment returns only the certificate, CA and host ID). The manager sends
    both names on the heartbeat ack, an additive proto field. An older manager
    sends nothing, and the page then shows the organization ID from the
    certificate and the host ID.
  - **What it collects:** every check it runs (assigned and local
    `agent.yaml` checks), each with its plugin, interval, last run, last result,
    and the **values as last sent to the manager**. Errors are shown as sent.
    Raw values are not trimmed: if a check sends process names or paths, the
    page shows them.
  - **Also sent:** the facts the agent reports (`AgentMessage.facts`), the
    time of the last report and what it contained, and, for an agent that
    probes other hosts, each assigned probe with its target address and its
    last result. Anything the agent sends to the manager appears on the page.
  - **What else it may do:**
    - whether it probes other hosts (3.82, with the `deny_cidrs` in effect);
    - a fixed statement that the agent itself never runs playbooks or remote
      commands. Server management in Pulsix connects to the host over SSH,
      separately from the agent, and the agent cannot know whether that
      happens. *(My call: the code confirms the agent has no such path.)*
    - whether script checks are configured, with their command names (3.6j);
    - whether local checks and secrets come from `agent.yaml`, with secrets
      by name only.
  - **Buffer:** how many results are waiting for the manager, and the oldest.
  - **Log:** the last 100 lines of the agent log.
- The HTML page refreshes itself every few seconds.
- The page is in German or English, following the browser's
  `Accept-Language`, with English as the fallback.
- The agent card in the Pulsix GUI shows whether the status page is on, and
  where: "Status page: off / on, local only / on, network".

## Non-goals

- No write actions of any kind: no restart, no "measure now", no config edit,
  no log download beyond what the page shows.
- No sign-in with Pulsix accounts. The page never talks to the manager on the
  visitor's behalf.
- No history or charts. The page shows the current state and the last sent
  values only.
- Not a customer status page (v0.5).

## Anti-goals (a failure even if it works)

- A secret's value appearing anywhere on the page, in the JSON or in a shown
  log line.
- The page reachable from the network without a token, or a token sent in
  clear text over the network.
- The page on by default, or turned on by the manager. Only the machine's own
  `agent.yaml` turns it on.
- The page showing less than the agent sends. Transparency that hides things
  is worse than none.

## Owner decisions (2026-11-27)

1. **Purpose:** support and transparency for anyone on the machine who wants
   to know what the agent collects.
2. **Access: both.**
   - Off by default.
   - `status_page: {listen: "127.0.0.1:8742"}` in `agent.yaml` turns it on,
     on loopback, without a token.
   - A non-loopback listen address is accepted only together with a token in
     `agent.yaml`. Without a token the agent refuses that config and says why.
3. The playback and proposals below were approved as a whole ("passt"):
   HTTPS on the network, self-refresh, de/en, JSON variant, the "what else it
   may do" section, 100 log lines, secrets by name, the agent card line, no
   write actions, no Pulsix sign-in.
4. **Secrets inside sent values: replace them before sending.** A check value,
   check error or fact can contain a secret's value, for example a database
   error that repeats a connection string. The agent replaces every current
   `agent.yaml` secret value (4 characters or more, the same redactor as the
   log ring) with `[secret]` **before the result is sent**. The page and the
   manager then see the same text, and the secret never leaves the machine.
   This changes what the manager receives, and it applies with or without the
   status page. Tests pin it for check values, check errors, probe results
   and facts.
   **Narrowed by the owner on 2026-11-27:** the values of a fixed allowlist of
   connection fields that are not secret are not replaced: `host`, `port`,
   `user`, `username`, `database`, `socket`, `tls_server_name`, and the file
   paths `tls_ca`, `tls_cert` and `tls_key`. The builder adds the other
   plainly non-secret fields the agent's plugins read (`tls`, `pipe`,
   `sslmode`, `sslrootcert`, `sslcert`, `sslkey`, `basic_auth_user`; one list,
   `secrets.IsSecretField`). Any other field, an unknown one included, counts
   as secret.

## Configuration

A new top-level `agent.yaml` key `status_page`. It joins `secrets`,
`commands`, `checks` and `probes` (`internal/agent/secrets/parse.go`,
`topLevelKeys`).

```yaml
status_page:
  listen: "127.0.0.1:8742"     # required to turn it on; host:port
  token: "…"                   # required when listen is not loopback
  tls_cert: /path/cert.pem     # optional; network only
  tls_key:  /path/key.pem      # optional; network only
```

Rules:

- **Missing `status_page` or missing `listen`:** the page is off.
- **Loopback `listen`** (`127.0.0.0/8`, `::1`, `localhost`): plain HTTP, no
  token needed. A token set anyway is enforced.
- **Any other address:**
  - `token` is required; without one the config is refused.
  - The page is served over HTTPS only. With `tls_cert` and `tls_key` the agent
    uses them. Without them it generates a self-signed certificate once and
    keeps it in the data dir; the browser warns once.
- **Token rules:** a literal value in `agent.yaml`, which is already under
  the agent's file protection rules, at least 32 characters. It is compared in constant time and
  accepted as `Authorization: Bearer <token>` or through an HTML login form that
  sets a session cookie (HttpOnly, Secure, SameSite=Strict). The form is there
  so a browser can open the page.
- **Refused config:** an invalid `status_page` section turns only the page off.
  The reason is logged and shown as an `agent.yaml` problem. *My call:* today
  an error in `secrets`, `commands` or `probes` refuses the whole file. That
  drops every secret, command and local check, and probing fails closed. A
  status page error must not do that, so `status_page` gets its own
  per-section problem path, like local checks have per entry. Secrets, checks
  and probes stay in effect.

## Security notes

- **Response headers:** `Content-Security-Policy` without inline scripts from
  outside, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
  `frame-ancestors 'none'`.
- **Escaping:** every value is HTML-escaped. Check values and log lines are
  untrusted text.
- **Secrets in log lines:** the agent has no log redaction today and logs to
  stderr (the journal) by default. *My call:* a small in-memory ring of the
  last 100 lines is fed by a tee on the agent's logger. Before a line enters
  the ring, every occurrence of a current secret value of 4 characters or
  more is replaced by `[secret]`. The redactor holds the values, and the page
  model never does. Shorter values are not replaced, because they would
  mangle nearly every line. The docs say so.
- **Read-only:** GET and HEAD only, plus the POST of the login form; anything
  else gets 405.
- **Limits:** the server sets small timeouts and a connection limit. It must
  not be a way to slow the agent's monitoring.
- **Failed tokens:** they are rate-limited per remote address.

## Agent card

The heartbeat gains a field saying whether the status page is off, on
loopback, or on the network. The manager stores it on the host's agent record
in a new nullable column (additive migration). An older agent sends nothing,
and the card shows nothing.

- **Shown:** the agent card on the host page gets one line under "Probes
  other hosts" (3.82).
- **Not shown:** the address, port, or token.

## Platforms

The page works the same on Linux, Windows and macOS. The docs cover:

- firewall rules for a network listen address (Windows Firewall, firewalld or
  ufw, macOS pf);
- that the service user needs no extra rights for loopback, but a port below
  1024 needs privileges the agent does not have, so pick a higher one.

## Documentation

- **User docs:** a new section in `agent-configuration.md` for `status_page`, and
  a short "What the agent collects" page that points to it.
- **Changelog:** an entry under Unreleased.

## Left to the builder

- **Package layout:** a small `internal/agent/statuspage` package that reads a
  snapshot from the running agent through an interface, so it is testable with
  fakes.
- **Page design:** plain server-rendered HTML with an inline stylesheet, no SPA
  build.
- **Refresh:** by meta refresh or a small script; the CSP must allow whichever
  is chosen.
- **Exact JSON field names.**
- **Live change of `listen`:** whether a change applies on the agent's next
  `agent.yaml` reload or only on restart. Say which in the docs.

## Open questions

None at the time of writing.
