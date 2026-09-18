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
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# The worker talks gRPC only; CGO_ENABLED=0 keeps the build toolchain-free.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/pulsix-worker ./cmd/worker

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

# The worker's scratch root: per-job temp dirs and the persistent repo
# cache live under it (PULSIX_WORKER_DATA_DIR defaults to this path). It
# must be owner-private and writable by the runtime user. A token-enrolling
# worker (PULSIX_ENROLL_TOKEN, no PULSIX_WORKER_CERT/KEY/CA_CERT bundle)
# writes its identity directory under this same data volume by default
# (<data dir>/identity), so the enrolled and later renewed certificate
# survives a restart without any extra mount.
RUN mkdir -p /var/lib/pulsix-worker && chmod 0700 /var/lib/pulsix-worker \
	&& chown -R 65532:65532 /var/lib/pulsix-worker \
	&& mkdir -p /etc/pulsix-worker && chown 65532:65532 /etc/pulsix-worker
# /etc/pulsix-worker is where the compose stack mounts the worker's
# certificate bundle (writable: renewal replaces cert.pem there); owned by
# the runtime user so a fresh named volume first mounted here gets the
# same ownership the enrollment service's side of it has.
ENV PULSIX_WORKER_DATA_DIR=/var/lib/pulsix-worker
VOLUME ["/var/lib/pulsix-worker"]

USER 65532:65532
ENTRYPOINT ["/pulsix-worker"]
