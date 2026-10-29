# Running Pulsix with Docker Compose

A single-node deployment: the manager, a TimescaleDB database, one zone
worker in its own container (enrolled automatically), and an optional
Traefik reverse proxy for TLS.

## Quick start

```bash
cp .env.example .env
# edit .env: at minimum, set DB_PASSWORD and PULSIX_MASTER_KEY
# (openssl rand -hex 32 for each) and PULSIX_PUBLIC_URL.

docker compose up -d
```

`docker compose up` refuses to start if any required secret in `.env` is
missing or empty — see `.env.example` for what each variable does and why
it is required.

The manager waits for the database's healthcheck (`pg_isready`) before it
starts, runs its own migrations on startup, and is reachable at
`http://localhost:8080` (or whatever `PULSIX_PUBLIC_URL` names, once you
put something in front of it — see below). Database data lives in the
named volume `pulsix-db`, so it survives `docker compose down` (but not
`docker compose down -v`, which deliberately discards it).

On first start, visit `PULSIX_PUBLIC_URL` in a browser: a fresh instance
with no administrator yet redirects to the setup wizard.

## The worker

Jobs (playbook runs, ad-hoc commands, connection tests, bootstrap, key
rotation, repository syncs) run on a **worker**, never in the manager.
The stack ships one, in two services:

- `worker-enroll` — a one-shot service that runs the manager image's
  `worker add` command. It waits until the zone it enrolls for exists (on
  a fresh instance: until you have completed the setup wizard), issues the
  worker's certificate, registers the worker, writes the certificate
  bundle into the `pulsix-worker-bundle` volume and exits. On every later
  `docker compose up` it finds the bundle still valid and exits without
  registering another worker. It is the only service besides the manager
  that holds the database credentials and the master key, and it runs no
  playbooks.
- `worker` — the long-running worker. It has **no** database access and
  **no** master key, and waits for the bundle before it starts claiming
  jobs. The bundle volume is writable for it (not read-only): the worker
  renews its own certificate and replaces `cert.pem` in place.

Playbooks do not run as the worker. The image carries a second,
unprivileged account (uid 65533) and `docker-compose.yml` points
`PULSIX_WORKER_PLAY_UID` at it, so a task aimed at the worker's own
machine cannot read the certificate and key the worker holds. Set
`PULSIX_WORKER_PLAY_UID=` (empty) in `.env` to turn that off; playbooks
then run as the worker, as they did before v0.4. The account and the
small setuid helper that reaches it exist only inside this image — a
worker you build yourself has to ship both or leave the variable empty.

Cancelling a run stops the playbook on the worker, with everything it
started there, and starts nothing new on any host. It cannot take back a
command already running on a target host, which may continue there; the
run view marks those hosts as interrupted. Check them before you start
the run again. The worker service runs with `init: true` so that nothing a
run leaves behind lingers as a defunct process; add `--init` if you start
the worker image with a plain `docker run`.

By default the worker serves the `default` zone of the first
organization. `PULSIX_WORKER_ZONE`, `PULSIX_WORKER_ORG` and
`PULSIX_WORKER_NAME` in `.env` change that before the first enrollment.

**`PULSIX_WORKER_ORG` must be set whenever more than one organization
exists at the time of enrollment** (every organization has its own
`default` zone, so the slug alone is ambiguous). Without it `worker-enroll`
exits with `zone slug "default" is used by N organizations; pass --org
<org-slug>`, retries at most three times and then stays stopped — set
`PULSIX_WORKER_ORG` in `.env` and run `docker compose up -d` again. A worker
that is already enrolled is not affected: its valid bundle is reused
before the zone is looked up.

What v0.2 offers for workers today:

- A worker's registry entry exists from the moment it is enrolled (the
  `worker add` command registers it); it reports in on every job claim.
- Zones are created per organization in the web interface; every
  organization starts with a `default` zone.
- The Workers page (owners and admins) lists every worker and revokes
  or deletes it. A revoked worker's certificate stops working at once.

A worker only ever claims jobs of the one zone of the one organization it
was enrolled for.

### Re-enrolling

To enroll the bundled worker afresh (for example for another zone),
remove its bundle and rerun the enrollment:

```bash
docker compose rm -sf worker worker-enroll
docker volume rm <project>_pulsix-worker-bundle   # e.g. compose_pulsix-worker-bundle
docker compose up -d
```

### If the bundled worker was revoked

After the bundled worker is revoked, `worker-enroll` stops with a message
naming it instead of silently enrolling it again: re-enrolling on its own
would undo the revocation. To bring it back, delete the revoked worker on
the Workers page, then recreate both services — the running `worker`
container still holds the old certificate in memory:

```bash
docker compose up -d --force-recreate worker-enroll worker
```

## Additional workers

To add a worker for another zone, or a second worker for more
throughput, enroll it with the manager image's `worker add` command. The
image's entrypoint already is the manager binary, so pass only the
subcommand:

```bash
# Prints three PEM blocks and a usage note to stdout:
docker compose run --rm --no-deps manager worker add --zone <slug> --name <name>
```

Pass `--org <org-slug>` too if more than one organization uses the same
zone slug. Save the three blocks as `cert.pem` (the "# client
certificate" block), `key.pem` (the "# private key" block) and `ca.pem`
(the "# CA certificate" block) on the worker's machine, then run the
worker image with:

| Variable | Value |
|---|---|
| `PULSIX_MANAGER_ADDR` | the manager's worker endpoint, `host:8444` |
| `PULSIX_MANAGER_SERVER_NAME` | the manager's `PULSIX_GRPC_SERVER_NAME` (default `pulsix-manager`) |
| `PULSIX_WORKER_CERT`, `PULSIX_WORKER_KEY`, `PULSIX_CA_CERT` | the three file paths |

The worker image runs as uid 65532, not root. A bind-mounted directory
(unlike a named volume) keeps whatever ownership it already has on the
host, so make it writable by that uid before the first start — the
worker also renews its own certificate back into the same directory, not
just reads it once:

```bash
mkdir -p worker-certs
mv cert.pem key.pem ca.pem worker-certs/
sudo chown -R 65532:65532 worker-certs
```

A worker on this same compose network uses `manager:8444` as its address;
copy the `worker` service in `docker-compose.yml` under a new name with
its own bind-mounted bundle directory (owned the same way).

### Workers on other machines

The worker endpoint is not published on the host by default. To serve a
worker elsewhere, publish it in a `docker-compose.override.yml`:

```yaml
services:
  manager:
    ports:
      - "8444:8444"
```

Then enroll the remote worker with a one-time token instead of a copied
certificate bundle:

1. On the Workers page, "Add worker" → pick the zone and a name → copy the
   command it prints.
2. Run that command on the worker's machine:

   ```bash
   docker run -d --name pulsix-worker-hel-1 --restart unless-stopped \
     -e PULSIX_MANAGER_ADDR=pulsix.example.com:8444 \
     -e PULSIX_MANAGER_SERVER_NAME=pulsix-manager \
     -e PULSIX_ENROLL_TOKEN=pxw1.… \
     -v pulsix-worker-hel-1:/var/lib/pulsix-worker \
     noixdev/pulsix-worker:<version>
   ```

3. The token is single use and valid for one hour. After the first start
   the worker's identity lives in the named volume, and the token is
   ignored on every later start. To re-enroll, remove the volume and use a
   new token from the Workers page.
4. Revoking the worker on the Workers page cuts it off at once.

`PULSIX_MANAGER_SERVER_NAME` is required whenever the address you dial is
not the manager's `PULSIX_GRPC_SERVER_NAME` itself: the endpoint's
certificate carries that name, not your public hostname. The endpoint
only accepts workers this instance enrolled or issued a certificate for.

If you cannot redeem a token within its one-hour window right now (no one
is watching a browser session, or you are scripting bulk enrollment),
use the `worker add` bundle flow from "Additional workers" above instead
and copy the three files over by hand — it has no time limit.

## The embedded worker (opt-in, trusted single-tenant setups only)

`PULSIX_EMBEDDED_WORKER=1` runs a worker inside the manager process
instead of (or in addition to) the `worker` service. **Leave it off
unless every person who can write playbooks on this instance is trusted
with the whole instance:** an embedded worker runs playbooks inside the
manager container, as the manager's user, next to `PULSIX_MASTER_KEY` and
the database credentials — a playbook task on `localhost` can read them.
The manager logs a warning at startup whenever it is on. Further limits:

- The manager image has no Ansible runtime, so an embedded worker can run
  connection tests only; every other job kind fails.
- It serves only the `default` zone, and only on an instance with a
  single organization.
- Every manager start registers a new worker entry named `embedded`, and
  if enrollment fails after startup it stays stopped until the next
  restart.

## TLS with the bundled Traefik (optional)

The `traefik` service is not started by a plain `docker compose up` — it
only runs with the `proxy` profile:

```bash
# also set PULSIX_DOMAIN and TRAEFIK_ACME_EMAIL in .env first
docker compose --profile proxy up -d
```

This terminates TLS with a Let's Encrypt certificate and proxies to the
manager over plain HTTP internally. If you already run your own reverse
proxy or load balancer, skip this profile entirely and point it at the
manager's published `8080` port instead — the two are mutually exclusive
ways of getting traffic to the same manager container, not sequential
steps.

### Client addresses behind a proxy

Behind any reverse proxy the manager sees the proxy as the caller unless
it trusts that proxy's `X-Forwarded-For` header. `PULSIX_TRUSTED_PROXIES`
lists the proxies it trusts (CIDRs or addresses, comma separated).

- **Bundled Traefik:** nothing to do. Traefik gets the fixed address
  `10.213.73.254` on the stack's network (`PULSIX_PROXY_ADDR`), outside the
  range other containers draw from, and `PULSIX_TRUSTED_PROXIES` defaults
  to exactly that address.
- **Your own proxy:** set `PULSIX_TRUSTED_PROXIES` to the address the
  manager sees it connect from. Without it, webhook IP allowlists never
  match (every delivery comes "from the proxy"), audit entries record the
  proxy's address, and every webhook caller shares one per-address budget
  for failed calls. Verified webhook deliveries are never refused by that
  budget, but callers with a wrong id or signature are.
- Never trust a range untrusted clients can connect from: a trusted peer
  can claim any client address.
- The stack's network uses the fixed subnet `10.213.73.0/24`. If that
  collides with a network of yours, change `PULSIX_NETWORK_SUBNET`,
  `PULSIX_NETWORK_RANGE` and `PULSIX_PROXY_ADDR` together (see
  `.env.example`). Changing them recreates the network on the next
  `docker compose up -d`.

## Upgrading

```bash
# in .env: set PULSIX_VERSION to the release you want (e.g. v0.2.0)
docker compose pull
docker compose up -d
```

Since v0.2c the stack's network has a fixed subnet (see "Client addresses
behind a proxy"). The first `docker compose up -d` after that upgrade
recreates the network; if your Compose version refuses to, run
`docker compose down` (volumes stay) and then `docker compose up -d`.

The manager runs its own migrations on every start, so an upgrade is just
"pull the new images, restart the containers" — there is no separate
migration step to run by hand.

## Tearing down

```bash
docker compose down       # stop everything, keep the database and worker volumes
docker compose down -v    # stop everything AND discard all volumes (database included)
```

## Files

- `docker-compose.yml` — the stack itself.
- `.env.example` — every variable it reads, and why each one is required
  or optional.
- `docker-compose.test.yml` (this directory's sibling) is unrelated to
  this file: it is the throwaway stack `web/e2e/global-setup.ts` starts
  for the Playwright suite, not something an operator runs.
