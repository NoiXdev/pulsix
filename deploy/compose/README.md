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

The wizard's first step asks for the **setup token**, so whoever reaches a
fresh instance before you cannot claim it. The token is derived from
`PULSIX_MASTER_KEY` (it stays the same across restarts) and is only needed
until setup is completed. While setup is not completed, the manager logs
it at every start, together with a link that opens the wizard with the
token already filled in:

```sh
docker compose logs manager | grep -i "setup token"
```

Or print the token and the link on demand:

```sh
docker compose run --rm cli setup-token
```

Once setup is completed, the manager no longer logs a token and
`setup-token` says that none is needed.

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
  or deletes it. A revoked worker's certificate stops working at once
  (restoring a backup taken before the revocation brings it back; see
  "Restoring").

A worker only ever claims jobs of the one zone of the one organization it
was enrolled for.

### Re-enrolling

To enroll the bundled worker afresh (for example for another zone),
remove its bundle and rerun the enrollment. `worker-enroll` runs with
`--replace`, so it revokes and deletes the old registration of the same
name and enrolls the worker again:

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
throughput, enroll it with the manager image's `worker add` command,
through the stack's one-off `cli` service (see "Commands" below). The
image's entrypoint already is the manager binary, so pass only the
subcommand:

```bash
# Prints three PEM blocks and a usage note to stdout:
docker compose run --rm --no-deps cli worker add --zone <slug> --name <name>
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
   PULSIX_ENROLL_TOKEN=pxw1.… docker run -d --init --name pulsix-worker-hel-1 --restart unless-stopped \
     -e PULSIX_MANAGER_ADDR=pulsix.example.com:8444 \
     -e PULSIX_MANAGER_SERVER_NAME=pulsix-manager \
     -e PULSIX_ENROLL_TOKEN \
     -e PULSIX_WORKER_PLAY_UID=65533 \
     -v pulsix-worker-hel-1:/var/lib/pulsix-worker \
     noixdev/pulsix-worker:<version>
   ```

   The token is the docker client's environment (`PULSIX_ENROLL_TOKEN=… docker
   run`) and a bare `-e PULSIX_ENROLL_TOKEN` hands it on, not
   `-e PULSIX_ENROLL_TOKEN=pxw1.…`: an argument shows in the process list to
   every local user of the machine. The dialog's Compose tab works the same
   way: its file only names `${PULSIX_ENROLL_TOKEN:-}`, and the token is set
   on the start command (`PULSIX_ENROLL_TOKEN=pxw1.… docker compose up -d`),
   so the file can be saved or committed without a credential in it. Later
   commands on the file (`docker compose ps`, `down`, `pull && up -d`) need
   no token: the variable is simply empty then.

3. The token is single use and valid for one hour. After the first start
   the worker's identity lives in the named volume, and the token is
   ignored on every later start. To re-enroll, remove the volume and use a
   new token from the Workers page.
4. Revoking the worker on the Workers page cuts it off at once, until a
   backup taken before the revocation is restored (see "Restoring").

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
- The manager service runs with `init: true` for the same reason the
  `worker` service does: nothing the embedded worker leaves behind
  lingers as a defunct process. Add `--init` if you start the manager
  image with a plain `docker run` and turn the embedded worker on.

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

## Commands

The manager's commands (`backup`, `restore`, `worker add`) run in the
`cli` service: the manager's image, environment and volumes, without the
manager's proxy labels and ports, so a command never receives web
traffic. It belongs to the `cli` profile, so `docker compose up` never
starts it; `docker compose run --rm cli <command>` does, together with the
database if that is not running yet. Run commands through `cli`, not
`manager`: a one-off `manager` container carries the proxy labels, and
with the `proxy` profile active Traefik would send part of the requests
to it for as long as the command runs.

## Backup and restore

Everything Pulsix keeps lives in the database, so a backup is the
database's content: every organization, user, host, setting and secret,
the instance CA included, and optionally the metric history. Every backup
is encrypted with a key derived from `PULSIX_MASTER_KEY`, and the master
key itself is **not** in it: keep your `.env` (or at least the master key)
somewhere else than the backups. Without it no backup can be restored.

Backups go to the `pulsix-backups` volume (`/var/lib/pulsix/backups` in the
manager and `cli` containers) unless the web interface names S3-compatible storage
(*Settings → Backups*, which also sets the daily schedule and how
many backups to keep). A backup folder must not be shared between Pulsix
instances: each one treats the backup files in it as its own.

### Taking a backup

```bash
# To the destination set in the web interface (by default the volume):
docker compose run --rm cli backup
# Without the metric history:
docker compose run --rm cli backup --without-metrics
# To a bucket, whatever the web interface says. Export the two keys in your
# shell first; -e NAME without a value passes them through, so the secret
# never appears on the command line. Endpoint, region and path style are not
# secret and go on the command line as values:
export PULSIX_BACKUP_S3_ACCESS_KEY=… PULSIX_BACKUP_S3_SECRET_KEY=…
docker compose run --rm \
  -e PULSIX_BACKUP_S3_ENDPOINT=https://s3.example.net -e PULSIX_BACKUP_S3_PATH_STYLE=true \
  -e PULSIX_BACKUP_S3_ACCESS_KEY -e PULSIX_BACKUP_S3_SECRET_KEY \
  cli backup --to s3://bucket/prefix
```

The command works while the stack runs and prints the backup's name and
size. `PULSIX_BACKUP_S3_REGION` is optional; without
`PULSIX_BACKUP_S3_ENDPOINT` the bucket is on AWS S3;
`PULSIX_BACKUP_S3_PATH_STYLE=true` addresses the bucket as
`endpoint/bucket` (MinIO and most self-hosted services need it).

`docker compose run --rm cli backup key` prints the passphrase the
backups are encrypted with. With it, the stock `age` tool decrypts a
backup without Pulsix (`tail -n +3 FILE.pxbak | age -d | gunzip`). Keep it
as secret as the master key.

### Restoring

Restore needs the manager stopped; it refuses while one is connected to
the database, and checks again right before it clears the database.

```bash
docker compose run --rm cli restore --list    # name, time, kind, version, size
docker compose down                           # stops the stack; the volumes stay
docker compose run --rm cli restore pulsix-20261102T030000Z-scheduled.pxbak
docker compose up -d
```

- **A database that is in use** (it has users or organizations) is only
  replaced with `--replace`, which asks you to type `restore`. In a script,
  add `--yes`.
- **A downloaded backup** goes in on standard input (note `-T`):
  `docker compose run --rm -T cli restore - < pulsix-….pxbak`.
  Standard input cannot answer the question, so replacing a database this
  way needs `--replace --yes`:
  `docker compose run --rm -T cli restore --replace --yes - < pulsix-….pxbak`.
- **From a bucket:** `restore --list --from s3://bucket/prefix` and
  `restore --from s3://bucket/prefix <name>`, with the endpoint (and path
  style) as values and the two exported keys passed through, as above:

  ```bash
  docker compose run --rm \
    -e PULSIX_BACKUP_S3_ENDPOINT=https://s3.example.net -e PULSIX_BACKUP_S3_PATH_STYLE=true \
    -e PULSIX_BACKUP_S3_ACCESS_KEY -e PULSIX_BACKUP_S3_SECRET_KEY \
    cli restore --from s3://bucket/prefix --replace pulsix-20261102T030000Z-scheduled.pxbak
  ```

  Restore never reads the credentials from the database it replaces; the
  Restore… dialog in the web interface shows this command with your saved
  endpoint, region and path style filled in.

Before it changes anything, restore reads the whole backup and checks it:
a damaged file, a file from a newer Pulsix or a different master key is
refused and the database stays as it was. If the restore fails after that
(the connection drops, say), it says so: the database is then empty, and
running the same command again finishes the job.

On success restore prints what it restored. Start the stack. Workers and
agents reconnect on their own. Enroll again: a worker or agent enrolled
after the backup; one whose certificate was issued before the backup and
has expired since (certificates renew about every 60 days); and, after
restoring a backup taken before agent key fingerprints were recorded, an
agent that renewed its certificate after the backup. Each one's log says to
enroll anew; enroll them from the Agents or Workers page. The bundled
`worker` re-enrolls itself: `worker-enroll` enrolls it again, and the
running worker picks up the new bundle without being recreated. A backup
from an older Pulsix is migrated to the current version as part of the
restore.

A restore ends every browser session: everyone signs in again. Everything
else that grants access is as it was when the backup was taken:
passwords, passkeys, second factors (TOTP and recovery codes), API tokens,
agent and worker enrollment tokens, open invitations, worker and agent
certificates, organization memberships and disabled users. A password
changed after the backup works with its old value again; a passkey,
second factor, recovery code, API token, invitation or certificate
removed, used or revoked after the backup is valid again; a member removed
or a user disabled after the backup is back. An enrollment token used up
or withdrawn after the backup is redeemable again until it expires:
withdraw pending ones on the Agents and Workers pages. After a restore,
check the users, members, invitations, API tokens, enrollment tokens,
workers and agents, and make each of those changes again.

### Moving to a new server

1. Install the stack on the new server with the **same**
   `PULSIX_MASTER_KEY` in `.env` (copy the old `.env`, adjusting what
   differs).
2. Restore before the first `docker compose up`:
   `docker compose run --rm -T cli restore - < pulsix-….pxbak`
   (or by name from the bucket). If the new instance was already set up,
   stop it with `docker compose down` and add `--replace --yes`.
3. `docker compose up -d`. The new server has no worker bundle yet, so
   `worker-enroll` enrolls the bundled worker again under its old name,
   replacing the registration the backup brought along. Workers and
   agents on your hosts keep their certificates and reconnect on their
   own once the new server is reachable under the address they know.
   Enroll again, as above: a worker or agent enrolled after the backup;
   one whose certificate was issued before the backup and has expired
   since; and, after restoring a backup taken before agent key
   fingerprints were recorded, an agent that renewed its certificate after
   the backup.

## Upgrading

The rules behind this are in `docs/upgrades.md` in the Pulsix repository.
In short: the manager
migrates the database by itself on every start, migrations only go up, and
the way back is a backup.

1. **Take a backup first.** It is the only way back (see "No downgrade"
   below):

   ```bash
   docker compose run --rm cli backup
   ```

2. **Manager and bundled worker.** In `.env`, set `PULSIX_VERSION` to the
   release you want (e.g. `0.2.0`; image tags carry no leading `v`), then:

   ```bash
   docker compose pull
   docker compose up -d
   ```

   There is no separate migration step: the manager runs its migrations
   when it starts.
3. **Workers you added by hand** keep the image they were started with
   (`noixdev/pulsix-worker:<version>`). Pull the new image and recreate each
   container.
4. **Agents last**, host by host. The manager serves the agent binary of
   its own release; replace the installed one and restart the service:

   ```bash
   MANAGER=https://pulsix.example.net      # your manager's public URL
   ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
   d=$(mktemp -d)
   if curl -fsSL -o "$d/pulsix-agent"        "$MANAGER/agents/bin/linux/$ARCH" \
      && curl -fsSL -o "$d/pulsix-agent.sha256" "$MANAGER/agents/bin/linux/$ARCH.sha256" \
      && [ "$(sha256sum "$d/pulsix-agent" | cut -d ' ' -f1)" = "$(cut -d ' ' -f1 "$d/pulsix-agent.sha256")" ]; then
     sudo install -m 0755 "$d/pulsix-agent" /usr/local/bin/pulsix-agent && sudo systemctl restart pulsix-agent
   else
     echo "download failed or checksum mismatch: nothing installed" >&2
   fi
   rm -rf "$d"
   ```

   The agent keeps its identity (`/etc/pulsix`) and reconnects on its own;
   it needs no new token. Do not run the install script again for an update:
   it asks for an enrollment token, overwrites `/etc/pulsix/agent.env`, and
   its `systemctl enable --now` does not restart a running service, so the
   old binary would keep running. There is no automatic agent update yet.

Roll in that order: manager, then workers, then agents. A worker or agent
one minor version behind the manager (v0.1.x under v0.2.x) is supported.
Further behind, or ahead of the manager, it still works but the Workers
page, the Agents page and the host's agent card show a badge, and the
manager logs one warning for it:

- **Update required**: two or more minor versions (or a major) behind the
  manager. Update it.
- **Newer than manager**: ahead of the manager, usually because the manager
  was rolled back or is not updated yet. Update the manager.

Development builds (`dev`) never show a badge.

### No downgrade

Migrations never go down. A manager that finds the database at a schema
newer than it knows refuses to start, with "the database schema is at
version N, this Pulsix knows up to M: run the newer version, or restore a
backup taken by this one". To go back to an older release, stop the stack,
set `PULSIX_VERSION` to the old release and restore the backup taken before
the upgrade over the database, which is in use and so needs `--replace`
(see "Restoring"):

```bash
docker compose down
# in .env: set PULSIX_VERSION back to the old release
docker compose run --rm cli restore --replace pulsix-….pxbak   # asks you to type "restore"
docker compose up -d
```

Everything written since that backup is lost, including workers and agents
enrolled after it.

### Network (since v0.2c)

Since v0.2c the stack's network has a fixed subnet (see "Client addresses
behind a proxy"). The first `docker compose up -d` after that upgrade
recreates the network; if your Compose version refuses to, run
`docker compose down` (volumes stay) and then `docker compose up -d`.

## Tearing down

```bash
docker compose down       # stop everything, keep the database, backup and worker volumes
docker compose down -v    # stop everything AND discard all volumes (database and backups included)
```

## Files

- `docker-compose.yml` — the stack itself.
- `.env.example` — every variable it reads, and why each one is required
  or optional.
- `docker-compose.test.yml` (this directory's sibling) is unrelated to
  this file: it is the throwaway stack `web/e2e/global-setup.ts` starts
  for the Playwright suite, not something an operator runs.
