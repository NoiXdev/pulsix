# Contributing to Pulsix

Thanks for your interest in contributing.

## Before you start

Pulsix is developed in the open but led by NoiXdev. For anything larger than a small
fix, please open an issue first so we can agree on the approach. The project's
conventions — code structure, tests, migrations — are in [CLAUDE.md](CLAUDE.md); the
backlog is [docs/BACKLOG.md](docs/BACKLOG.md).

## Building and testing

```bash
make test        # the race-enabled test suite, in groups (see scripts/test-groups.sh)
make lint        # the server-side linters
make build       # the binaries and the web interface

cd web
npm ci
npm run lint     # the web interface's linter
npm test         # the web interface's tests
```

Every change comes with tests. CI must pass.

## Branching

- `main` — release-ready code; releases are cut from a green commit on `main` by the
  `release` workflow (started from the Actions tab), which also writes the
  changelog and the tag.
- Feature branches — created off `main`, named `feat/<topic>`, `fix/<topic>` or
  `docs/<topic>`. History stays linear: rebase onto `main` instead of merging it in.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

- `feat(worker): remove the repo caches of deleted repositories`
- `fix(api): refuse a subpath that leaves the repository`
- `docs: record 7.95`

## Pull requests

1. Create a branch off `main`.
2. Make your change, including tests.
3. Run `make test` and `make lint` (and the web tests for changes under `web/`).
4. Database migrations are additive only. A change to behaviour, configuration or the
   manager/worker/agent protocol gets a note in [docs/upgrades.md](docs/upgrades.md).
5. A change users can see needs an update of the user documentation at
   [docs.noix.dev/pulsix](https://docs.noix.dev/pulsix/admin/introduction/); mention it
   in the pull request.
6. Open the pull request against `main`. A maintainer will review.

## Reporting security issues

See [SECURITY.md](SECURITY.md) — do **not** file public issues for
vulnerabilities.
