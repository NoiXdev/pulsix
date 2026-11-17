# Agent binaries on the GitHub release (backlog 7.5)

Status: approved by the owner 2026-11-17 (approach A).

## Intent

Backlog 7.5 planned "agent binaries on GitHub releases, all from one `vX.Y.Z`
tag". The images ship to Docker Hub; the GitHub releases (v0.1.0-rc.2, rc.3)
carry no assets. Today an agent can only be fetched from a running manager
(`$MANAGER/agents/bin/<os>/<arch>` plus `.sha256`). Attaching the same
binaries to each release lets an operator get, verify and archive a version's
agents without a manager of that version, and closes the open half of 7.5.

## Goals

- Every release carries every agent binary the manager of that version
  serves, under the same names, with the same per-file `.sha256`.
- Built from the tagged commit with `VERSION=<tag>`, with the Go toolchain
  that tag's manager image uses, so the bytes match what the manager embeds.
- Existing releases (rc.2, rc.3) can be backfilled by hand.

## Non-goals

- No change to NoiXdev/github-workflows (other products use it by `@main`).
- No aggregate `SHA256SUMS`, no signing, no `.exe` names: the release mirrors
  the manager's download route exactly.
- No change to how agents are installed or upgraded; the manager stays the
  documented source.

## Anti-goals

- Overwriting a published asset with different bytes.
- Attaching binaries built from anything but the tag, or with another version
  string.

## Approaches

- **A — own job in Pulsix (chosen).** A reusable workflow
  `release-agents.yml` (`workflow_call` + `workflow_dispatch` with `tag`),
  called by `release.yml` after the shared release job. Shared workflow
  untouched; backfill comes for free. Cost: the release and its Discord
  announcement appear a few minutes before the assets; a failed upload leaves
  a release without assets until the job is re-run.
- **B — build hook in the shared workflow** (`go_version`, `asset_script`,
  attached via the existing `release_files`). Release complete at creation,
  dry runs rehearse it. Rejected: changes a workflow every product picks up
  at once, runs a Go build in the job holding the registry secrets, and still
  needs a separate path for the backfill.
- **C — reuse `version_script` + `release_files`.** Rejected: unpinned Go,
  bends the input's meaning.

## Design

- `.github/workflows/release-agents.yml`, inputs `tag` (required) and
  `dry_run` (call only, default false); `ref` (call only) is the commit to
  build on a dry run, where no tag exists yet.
  1. Validate `tag` against `^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$`.
  2. Checkout the tag (dry run: `ref`), SHA-pinned like `ci.yml`.
  3. Go version read from that checkout's `deploy/docker/Dockerfile.manager`
     (`FROM golang:<ver>-alpine`), passed to `actions/setup-go`.
  4. `scripts/agent-binaries.sh "$TAG"` — the tag's own copy, so each release
     gets exactly the targets its manager serves (rc.2: Linux only).
  5. Self-check: `go version -m` of every binary carries
     `-X main.version=$TAG`.
  6. Unless dry run: `scripts/release-agent-assets.sh "$TAG" <dist>` uploads.
- `scripts/release-agent-assets.sh` (POSIX sh, `gh` CLI): the release must
  exist; for each file in the dist (binary and `.sha256`), an asset of that
  name that is absent is uploaded; one that is present is downloaded and
  compared byte for byte — equal is skipped, different fails the job. So a
  re-run after a partial upload completes it, and nothing published is ever
  replaced.
- `release.yml`: job `agents`, `needs: [guard, release]`, `permissions:
  contents: write`, calls the workflow with the release's `tag`, `dry_run`
  and the guard's SHA. A dry run builds and verifies, uploads nothing.
- No environment: the job needs only `GITHUB_TOKEN`, no registry or webhook
  secret.

## Tests

- `scripts/release-agent-assets-test.sh`: drives the upload script against a
  fake `gh` on `PATH` (missing release, fresh upload, equal re-run skips,
  differing asset fails); run in CI's `go` job next to the agent-binaries
  step.
- `actionlint` and `shellcheck` clean.
- The workflow itself is proven only by a release or a backfill run; both
  need the owner's OK.

## Docs

- Backlog 7.5 row, `docs/upgrades.md` (roll order: the release as a second
  source), `deploy/compose/README.md` (Upgrading), comments in
  `release.yml`.
- noix-docs, Pulsix agent page: binaries and checksums for each version are
  also on the release page; feature branch until it ships.

## Left to the builder

Step names, exact `gh` invocations, fake-`gh` mechanics.
