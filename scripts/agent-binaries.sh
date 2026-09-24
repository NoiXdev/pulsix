#!/bin/sh
# agent-binaries.sh — cross-compile the agent binaries the manager serves.
#
# The manager embeds this directory (internal/agentdist) and hands the
# binaries out at GET /agents/bin/linux/{arch}, which is what the install
# script a host pipes into its shell downloads. A manager built without
# running this carries only the committed placeholder: it works, and the
# download route answers 503 naming this step. So this runs BEFORE
# `go build ./cmd/manager` — see the Makefile's `build` target and
# deploy/docker/Dockerfile.manager, which both call it.
#
# Output (the names internal/agentdist recognizes; keep them in lockstep
# with BinaryPrefix/ChecksumSuffix there — a test pins that):
#
#   internal/agentdist/dist/pulsix-agent-linux-amd64
#   internal/agentdist/dist/pulsix-agent-linux-amd64.sha256
#   internal/agentdist/dist/pulsix-agent-linux-arm64
#   internal/agentdist/dist/pulsix-agent-linux-arm64.sha256
#
# The checksum file is what install.sh verifies the download against
# before installing it, so it is written from the same bytes in the same
# run — never regenerated separately.
#
# Usage: scripts/agent-binaries.sh [version]
#   version defaults to $VERSION, then to "dev" (cmd/agent/main.go's own
#   default), and is stamped into the binary via -X main.version.
# POSIX sh, not bash: this also runs inside the manager image's build
# stage (deploy/docker/Dockerfile.manager), whose golang:alpine base has
# busybox ash and no bash.
set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
dist="$repo_root/internal/agentdist/dist"
version="${1:-${VERSION:-dev}}"

# sha256sum on Linux, shasum -a 256 on macOS: both print
# "<hash>  <name>", which is the format install.sh verifies against.
if command -v sha256sum > /dev/null 2>&1; then
	sha256="sha256sum"
elif command -v shasum > /dev/null 2>&1; then
	sha256="shasum -a 256"
else
	echo "agent-binaries: neither sha256sum nor shasum is available" >&2
	exit 1
fi

# Every target the agent ships for. install.sh maps uname -m (x86_64,
# aarch64/arm64) onto exactly these two; adding one here is all it takes
# to serve it, since the route builds its lookup from the request.
targets="linux/amd64 linux/arm64"

# Clear previous output so a target dropped from the list above cannot
# keep being served, and so a stale binary can never outlive the source
# it was built from. The committed .placeholder is left alone: it is what
# keeps `go:embed all:dist` compilable in a tree that never ran this
# script — and the glob below does not match it anyway, dotfiles being
# invisible to it (the case guard says so out loud regardless).
for stale in "$dist"/*; do
	[ -e "$stale" ] || continue
	case "${stale##*/}" in
	.placeholder) continue ;;
	esac
	rm -f "$stale"
done

for target in $targets; do
	goos="${target%/*}"
	goarch="${target#*/}"
	out="$dist/pulsix-agent-$goos-$goarch"
	echo "agent-binaries: building $goos/$goarch -> ${out#"$repo_root"/}"
	# Same flags as the manager image's build: static (no libc in the
	# distroless/scratch world the agent may land in either), -trimpath so
	# the binary carries no build-machine paths, -s -w to drop the symbol
	# table and DWARF a running agent never needs.
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
		-trimpath -ldflags="-s -w -X main.version=$version" \
		-o "$out" "$repo_root/cmd/agent"
	# "<hash>  <name>": the format install.sh's `cut -d " " -f1` reads and
	# `sha256sum -c` accepts, carrying the name the file is served under
	# (hence the cd: the checksum must not name a build-machine path).
	(cd "$dist" && $sha256 "$(basename "$out")" > "$(basename "$out").sha256")
done

echo "agent-binaries: $version written to ${dist#"$repo_root"/}"
