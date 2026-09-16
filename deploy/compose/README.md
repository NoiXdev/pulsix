# Running Pulsix with Docker Compose

A single-node deployment: the manager (with an embedded worker for its
default zone), a TimescaleDB database, an optional additional worker, and
an optional Traefik reverse proxy for TLS.

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

## The embedded worker

By default (`PULSIX_EMBEDDED_WORKER=1`) the manager container also runs a
worker in-process for the organization's default zone — this is what
"zero extra services" means: create your first organization through the
setup wizard and its default zone already has a worker to run jobs on,
with nothing else to start. It enrolls itself automatically a few seconds
after the first organization exists (it retries until one does) and shows
up in Settings → Workers as "embedded".

Set `PULSIX_EMBEDDED_WORKER=0` in `.env` to turn it off — for example once
you have enrolled dedicated per-zone workers and want the manager
container to only ever serve HTTP.

## Additional workers

The embedded worker only ever serves one organization's *default* zone.
To add a worker for another zone, or a second worker for more throughput,
enroll one and start the `worker` service:

```bash
# 1. Enroll it (prints three PEM blocks + a usage note to stdout):
docker compose run --rm manager /pulsix-manager worker add --zone <slug> --name <name>

# 2. Save the three PEM blocks under ./worker-certs/, one file per block:
mkdir -p worker-certs
#   ./worker-certs/cert.pem  — the "# client certificate" block
#   ./worker-certs/key.pem   — the "# private key" block
#   ./worker-certs/ca.pem    — the "# CA certificate" block

# 3. Start it:
docker compose --profile worker up -d worker
```

`--zone` takes a zone slug (`default` is the one every organization gets
automatically); pass `--org <org-slug>` too if more than one organization
uses the same zone slug. Run the `worker add` step again, with a
different `--name`, for each additional worker you want — each enrollment
issues its own certificate and registers its own row in Settings →
Workers.

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

## Upgrading

```bash
# in .env: set PULSIX_VERSION to the release you want (e.g. v0.2.0)
docker compose pull manager
docker compose up -d manager
```

The manager runs its own migrations on every start, so an upgrade is just
"pull the new image, restart the container" — there is no separate
migration step to run by hand.

## Tearing down

```bash
docker compose down       # stop everything, keep the database volume
docker compose down -v    # stop everything AND discard the database volume
```

## Files

- `docker-compose.yml` — the stack itself.
- `.env.example` — every variable it reads, and why each one is required
  or optional.
- `docker-compose.test.yml` (this directory's sibling) is unrelated to
  this file: it is the throwaway stack `web/e2e/global-setup.ts` starts
  for the Playwright suite, not something an operator runs.
