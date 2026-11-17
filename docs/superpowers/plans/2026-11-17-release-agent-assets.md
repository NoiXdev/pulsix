# Agent Binaries on the GitHub Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every Pulsix GitHub release carries the agent binaries and `.sha256` files its manager serves, built from the tag.

**Architecture:** A reusable workflow `release-agents.yml` builds the agents from the tag with the tag's own `scripts/agent-binaries.sh` and the Go version of the tag's manager Dockerfile, then a POSIX upload script attaches them through the `gh` CLI, skipping byte-identical assets and refusing differing ones. `release.yml` calls it after the shared release job; `workflow_dispatch` backfills old tags.

**Tech Stack:** GitHub Actions, POSIX sh, `gh` CLI, Go toolchain.

**Spec:** `docs/superpowers/specs/2026-11-17-release-agent-assets-design.md`

## Global Constraints

- Asset names exactly as the manager's dist: `pulsix-agent-<os>-<arch>` and `pulsix-agent-<os>-<arch>.sha256`, Windows without `.exe`. No `SHA256SUMS`.
- Tag pattern: `^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$`.
- A published asset is never replaced: identical bytes are skipped, different bytes fail.
- Actions SHA-pinned with the same pins as `ci.yml` (`actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`, `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`).
- Every `${{ }}` reaching a `run:` goes through `env:`, never inline (repo pattern).
- The agents job runs without an environment and with `contents: write` only.
- Scripts are POSIX sh (`#!/bin/sh`, `set -eu`), `shellcheck`-clean; workflows `actionlint`-clean.
- Conventional Commits, signed, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- A re-run after a partial upload (binary uploaded, its `.sha256` not): must complete, not fail on the existing asset.
- A tag with a prefix sibling (`v0.1.0-rc.1` vs `v0.1.0-rc.10`): the version self-check must match the whole tag, not a prefix. (As built: `-trimpath` drops `-ldflags` from the build info, so the check is a whole-tag grep of the binary, not `main.version`; the source is proven by a provenance check before the build, since the final review's fixes build with `-buildvcs=false` and the binaries carry no `vcs.*` stamp.)
- An empty dist (the script found nothing, only `.placeholder`): must fail, not "upload nothing" green.
- A tag that is not a release yet (dispatch with a typo, or a tag without a release): must fail before building.
- A dist file name with a space or glob character must not reach `gh release download --pattern` unquoted; the script only accepts names matching `pulsix-agent-[a-z0-9]*-[a-z0-9]*` (with optional `.sha256`) and fails on anything else.

---

### Task 1: Upload script with its test

**Files:**
- Create: `scripts/release-agent-assets.sh`
- Create: `scripts/release-agent-assets-test.sh`
- Modify: `.github/workflows/ci.yml` (the go job's `agent binaries` step)

**Interfaces:**
- Produces: `scripts/release-agent-assets.sh <tag> <dist-dir>`; uses `gh release view`, `gh release download`, `gh release upload`; repository from `GH_REPO` (as `gh` itself reads it). Exit 0 when every dist file is on the release with identical bytes; non-zero otherwise, with a message naming the file.

- [ ] **Step 1: Write the failing test** `scripts/release-agent-assets-test.sh` (bash, `set -euo pipefail`, like `scripts/worker-image-test.sh`). It puts a fake `gh` first on `PATH` that serves a release from a directory `$FAKE_RELEASE` (absent = no release): `release view <tag> --json assets --jq '.assets[].name'` lists the files; `release download <tag> --pattern <name> --dir <d>` copies one; `release upload <tag> <file>` copies it in and fails if the name exists (as real `gh` does without `--clobber`). Cases, each asserting exit status and resulting release contents:
  1. `no_release`: no `$FAKE_RELEASE` → non-zero, nothing written.
  2. `fresh_upload`: empty release, dist with `pulsix-agent-linux-amd64` + `.sha256` → zero, both present with identical bytes.
  3. `partial_rerun`: release holds the binary only → zero, `.sha256` added, binary untouched (fake records no upload of it).
  4. `differing_asset`: release holds a binary with other bytes → non-zero, message names `pulsix-agent-linux-amd64`, release unchanged.
  5. `empty_dist`: dist with only `.placeholder` → non-zero.
  6. `bad_tag`: tag `0.1.0` → non-zero before any `gh` call.
  7. `foreign_file`: dist holds `notes.txt` → non-zero.
  8. `missing_checksum`: binary without its `.sha256` → non-zero.

- [ ] **Step 2: Run it, expect failure** — `scripts/release-agent-assets-test.sh` → fails (script missing).

- [ ] **Step 3: Implement `scripts/release-agent-assets.sh`** — validate the tag pattern; list dist entries (dotfiles ignored), require ≥ 1 binary, every name matching the accepted pattern and every binary having its `.sha256`; fetch the release's asset names (failure = no release, stop); per file: absent → `gh release upload`; present → download into a `mktemp -d` and `cmp`; equal → log "already attached", different → fail. Header comment in the style of `scripts/agent-binaries.sh`.

- [ ] **Step 4: Run test and lint** — `scripts/release-agent-assets-test.sh` → all cases pass; `shellcheck scripts/release-agent-assets.sh scripts/release-agent-assets-test.sh` → clean.

- [ ] **Step 5: Wire into CI** — the go job's `agent binaries` step also runs `scripts/release-agent-assets-test.sh`; one comment line saying why.

- [ ] **Step 6: Commit** — `feat(release): add a script that attaches the agent binaries to a release`

### Task 2: Release workflows

**Files:**
- Create: `.github/workflows/release-agents.yml`
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: Task 1's `scripts/release-agent-assets.sh <tag> <dist-dir>`; the shared workflow's outputs `tag` (`needs.release.outputs.tag`); `needs.guard.outputs.sha`.
- Produces: `release-agents.yml` with `workflow_call` inputs `tag` (string, required), `dry_run` (boolean, default false), `ref` (string, default "") and `workflow_dispatch` input `tag` (string, required).

- [ ] **Step 1: Write `release-agents.yml`** — `permissions: contents: read` at the top; one job `agents`, `ubuntu-latest`, `timeout-minutes: 30`, `permissions: contents: write`, steps:
  1. Validate the tag against the Global Constraints pattern; on dispatch also `gh release view "$TAG"` must succeed (`GH_REPO: ${{ github.repository }}`).
  2. Checkout `ref: inputs.dry_run && inputs.ref || inputs.tag` — on a live run the tag, `fetch-depth: 1`. *(As built after the final review: `refs/tags/<tag>` on live and backfill runs, so a same-named branch cannot win; `persist-credentials: false` on both checkouts; then a provenance step: `HEAD` equals `refs/tags/$TAG^{commit}` (dry run: `inputs.ref`) and `git status --porcelain` is empty.)*
  3. Go version: `sed -n 's/^FROM golang:\([0-9][0-9.]*\)-alpine.*/\1/p' deploy/docker/Dockerfile.manager | head -n 1`, empty fails; to `$GITHUB_OUTPUT`; `setup-go` with it (`cache: false` — no `go.sum` caching concerns on a release build).
  4. `scripts/agent-binaries.sh "$TAG"`. *(As built after the final review: with `GOFLAGS=-buildvcs=false`. The manager image builds from a context without `.git/`, so its embedded agents carry no VCS stamp; built in a git checkout, the released bytes and checksums would differ from what the manager serves.)*
  5. Self-check: for every non-`.sha256` file in `internal/agentdist/dist/pulsix-agent-*`, `go version -m` output contains `main.version=$TAG"` (closing quote included, so `rc.1` does not match `rc.10`); also `sha256sum -c` of each `.sha256` from inside the dist. *(As built: `go version -m` has no `main.version` under `-trimpath`. After the final review the check per binary is: first line `go<ver>` from the Dockerfile, no `vcs` build line, `-trimpath=true` and `CGO_ENABLED=0`, the tag string found in the binary, a `.sha256` next to it, and `sha256sum -c`; an empty dist fails. On a non-dry run a further step pulls `docker.io/noixdev/pulsix-manager:<tag without v>` for linux/amd64, copies `/pulsix-manager` out and requires every dist file's bytes to occur in it (embed.FS stores files raw), so the assets are proven identical to what the manager serves.)*
  6. *(As built: the uploader comes from a second sparse checkout of the workflow's own commit, not the tag's checkout, so old tags can be backfilled.)* If not dry run: `scripts/release-agent-assets.sh "$TAG" internal/agentdist/dist` with `GH_TOKEN`, `GH_REPO`.
  Header comment: what it does, why separate from the shared workflow (owner decision 2026-11-17, spec link), backfill usage.

- [ ] **Step 2: Add the `agents` job to `release.yml`** — `needs: [guard, release]`, `permissions: contents: write`, `uses: ./.github/workflows/release-agents.yml`, `with: tag: needs.release.outputs.tag, dry_run: inputs.dry_run, ref: needs.guard.outputs.sha`. Header comment of `release.yml` gains one sentence on the agents job.

- [ ] **Step 3: Verify** — `actionlint .github/workflows/release.yml .github/workflows/release-agents.yml` → no output. Locally: `scripts/agent-binaries.sh v0.0.0-rc.1` then run step 5's check by hand → passes; with `v0.0.0-rc.10` against that dist → fails.

- [ ] **Step 4: Commit** — `feat(release): attach the agent binaries to every GitHub release`

### Task 3: Docs

**Files:**
- Modify: `docs/BACKLOG.md` (row 7.5; row 1.38's "still open in 7.5" phrase)
- Modify: `docs/upgrades.md` ("Roll order", step 3)
- Modify: `deploy/compose/README.md` ("Upgrading", step 4)
- noix-docs (`~/_dev/noix-docs`, new worktree on branch `feat/pulsix-agent-release-assets` from `origin/main`): `src/content/docs/pulsix/user/agents.md`

- [ ] **Step 1: Backlog** — 7.5 stays `planned` until a release has run with assets; its note says the job exists (commit), the backfill for rc.2/rc.3 awaits the owner, and what rc.2 gets (Linux only). 1.38's reference updated: the shared Go workflow is not needed for the agents.
- [ ] **Step 2: `docs/upgrades.md` and the compose README** — one sentence each: every release's page also carries the binaries and `.sha256` of that version, identical to what the manager serves; the manager stays the documented source.
- [ ] **Step 3: noix-docs** — one short paragraph on the agents page: each version's agent downloads and checksums are also on the release page (no tech-stack terms). Commit there on the feature branch.
- [ ] **Step 4: Commit** — `docs: document the agent binaries on the release page`
