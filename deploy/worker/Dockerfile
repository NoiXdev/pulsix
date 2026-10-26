# Dockerfile.worker builds the Pulsix zone worker image: the Go worker
# binary (claim loop + mTLS client) on top of an Ansible runtime carrying
# the newest ansible-core and ansible-runner.
#
# Build context must be the repository root:
#
#   docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .
#
# The run stage is a pinned python:3.13-slim with ansible-core and
# ansible-runner pip-installed at their newest stable versions (verified
# 2026-09-15 against PyPI: ansible-core 2.21.4, ansible-runner 2.4.3 — the
# runner's 2.x CLI is what the worker invokes; the reference image at
# quay.io/ansible/ansible-runner still ships a 2022 build of the 1.x
# runner, whose CLI differs, so the image carries its own pinned runtime).
# Ansible-core brings the ansible-playbook binary the runner locates via
# PATH; git + openssh-client + sshpass cover the worker's own duties (repo
# playbooks are cloned by the worker itself, and ssh_password credentials
# need sshpass).
#
# The invocation the worker pins (see internal/worker/runner/runner.go) is
# `ansible-runner run <dir> --json --playbook <path> --inventory <path>
# --ident <id>` — ansible-runner >= 2.x (the 1.x `stream` subcommand no
# longer exists).
#
# Base image pins were the newest stable tags on their registries as of
# 2026-09-15, pinned by tag *and* digest (the same policy the manager
# image follows):
#   - golang:1.27.1-alpine — matches go.mod's `go 1.27` (same digest as the
#     manager image's build stage)
#   - python:3.13-slim — Debian trixie slim, multi-arch index digest

# ---- Stage 1: build the Go worker binary ----------------------------------
FROM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
WORKDIR /src
# gcc + musl-dev build the setuid helper below. They exist in the BUILD
# stage only: a compiler in the run stage would be a tool for whatever a
# playbook's localhost task wants to build there.
RUN apk add --no-cache gcc musl-dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# The worker talks gRPC only; CGO_ENABLED=0 keeps the build toolchain-free.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/pulsix-worker ./cmd/worker

# The setuid helper that lets the unprivileged worker start a play under a
# second uid (see deploy/worker/setuid-exec.c for the whole argument). The
# two uids and the gid are compiled IN: the running program takes nothing
# from its arguments or environment that decides whose identity it becomes.
# -static on purpose — a setuid binary with no dynamic loader has no loader
# to subvert. -Werror because a warning in a setuid program is a finding.
RUN cc -static -Wall -Wextra -Werror -O2 \
		-DPLAY_UID=65533 -DWORKER_GID=65532 \
		-o /out/pulsix-setuid-exec deploy/worker/setuid-exec.c

# ---- Stage 2: the Ansible runtime the worker drives -----------------------
FROM python:3.13-slim@sha256:9d2e5553305c7c7b0097999bb17187c69b921ccd6bc9d40e4bb5ebe652c00285

# git + openssh-client: repo playbooks are fetched by the worker itself;
# sshpass: ansible_password credentials (Task 15's ad-hoc runs may hit
# password credentials); ca-certificates: https remotes.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends git openssh-client sshpass ca-certificates \
	&& rm -rf /var/lib/apt/lists/*

# The Ansible runtime, version-pinned: re-pinning is a deliberate,
# documented change (a new ansible-core changes behavior of a running
# fleet's playbooks in visible ways).
RUN pip install --no-cache-dir "ansible-core==2.21.4" "ansible-runner==2.4.3"

COPY --from=build /out/pulsix-worker /pulsix-worker

# The runtime user, with a passwd entry: OpenSSH looks the running uid up
# before it does anything else and refuses to start without one ("No user
# exists for uid 65532"), which broke every SSH playbook run and every git
# over SSH in the v0.2b image. The home holds nothing a run depends on —
# runs get their own HOME and known_hosts under their work dir — and it
# stays out of the data volume; the shell is nologin (nobody logs in).
#
# pulsix-play (65533) is the second uid a playbook runs under when
# PULSIX_WORKER_PLAY_UID is set. Its PRIMARY group is the worker's own
# (65532), not a group of its own: the run's work dir is shared by group, so
# everything the play writes stays group-owned by the worker and readable
# back — that is what keeps the run's event stream working. It owns nothing
# else and cannot log in. What it specifically does NOT reach is the
# worker's identity directory, which stays owner-only below.
#
# Its home DOES have to exist, and that was learned the hard way: Ansible
# expands `remote_tmp` (`~/.ansible/tmp`) from the account's passwd entry,
# not from $HOME, and does it for a `connection: local` task as much as for
# an SSH one. With --no-create-home every task came back UNREACHABLE with
# "Failed to create temporary directory ... /home/pulsix-play/.ansible/tmp".
# The run's own scratch still lives in its work dir (HOME, ANSIBLE_CONFIG
# and ANSIBLE_LOCAL_TEMP all point there, and the dir is wiped afterwards);
# this one is 0700 so nothing but the play itself can look inside it.
RUN groupadd --system --gid 65532 pulsix \
	&& useradd --system --uid 65532 --gid 65532 --home-dir /home/pulsix --create-home \
		--shell /usr/sbin/nologin pulsix \
	&& chmod 0700 /home/pulsix \
	&& useradd --system --uid 65533 --gid 65532 --home-dir /home/pulsix-play --create-home \
		--shell /usr/sbin/nologin pulsix-play \
	&& chmod 0700 /home/pulsix-play

# The privileged step, and the only setuid binary in the image. root
# ownership is what makes the setuid bit mean root; the group is the
# worker's (65532) and the mode is 4750, so only the worker may invoke it
# at all.
#
# WHY NOT 4755: the helper confers gid 65532 — the worker's own group — as
# well as uid 65533, so "anyone may run it" is only free while nothing
# worker-owned is group-accessible. That stopped being true. A live run's
# work dir is 2770 with the job's SSH keys and vault passwords at 0660
# inside it, and the repo cache is 0750 whenever the separation is on, so
# a 4755 helper would hand every account in the image a way into both.
# 4750 costs nothing — the worker is the only caller there has ever been —
# and it removes the question entirely.
COPY --from=build /out/pulsix-setuid-exec /usr/local/bin/pulsix-setuid-exec
RUN chown root:pulsix /usr/local/bin/pulsix-setuid-exec \
	&& chmod 4750 /usr/local/bin/pulsix-setuid-exec

# The worker's scratch root: per-job temp dirs and the persistent repo
# cache live under it (PULSIX_WORKER_DATA_DIR defaults to this path). It
# must be owner-private and writable by the runtime user. A token-enrolling
# worker (PULSIX_ENROLL_TOKEN, no PULSIX_WORKER_CERT/KEY/CA_CERT bundle)
# writes its identity directory under this same data volume by default
# (<data dir>/identity), so the enrolled and later renewed certificate
# survives a restart without any extra mount.
RUN mkdir -p /var/lib/pulsix-worker && chmod 0700 /var/lib/pulsix-worker \
	&& chown -R 65532:65532 /var/lib/pulsix-worker \
	&& mkdir -p /etc/pulsix-worker && chown 65532:65532 /etc/pulsix-worker \
	&& chmod 0700 /etc/pulsix-worker
# /etc/pulsix-worker is where the compose stack mounts the worker's
# certificate bundle (writable: renewal replaces cert.pem there); owned by
# the runtime user so a fresh named volume first mounted here gets the
# same ownership the enrollment service's side of it has. Mode 0700, not
# the 0755 a bare mkdir leaves: this directory holds the worker's client
# key, and world-readable is how a localhost play would read it. The worker
# chmods it again at start-up when PULSIX_WORKER_PLAY_UID is set, but that
# only repairs an existing deployment — a fresh image must be right before
# any of its code runs.
ENV PULSIX_WORKER_DATA_DIR=/var/lib/pulsix-worker
VOLUME ["/var/lib/pulsix-worker"]

# PULSIX_WORKER_PLAY_UID is deliberately NOT set here. The image ships
# everything the separation needs — the pulsix-play account, the shared
# group, the setuid helper — so a deployment can turn it on with one
# variable, and deploy/compose/docker-compose.yml sets it for a new
# installation. Baking it into the image instead would opt every existing
# deployment in at its next `docker compose pull`, including ones on a
# bind-mounted data directory or a derived image where uid 65533 does not
# exist; the design requires those to keep working unchanged until their
# operator opts in.

USER 65532:65532
ENTRYPOINT ["/pulsix-worker"]
