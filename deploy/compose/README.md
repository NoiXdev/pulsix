# Running Pulsix with Docker Compose

A single-node deployment: the manager, a TimescaleDB database, and an
optional Traefik reverse proxy for TLS.

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
