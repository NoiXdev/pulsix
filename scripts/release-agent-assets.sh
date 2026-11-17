#!/bin/sh
# release-agent-assets.sh — attach the agent binaries to a GitHub release.
#
# The manager serves the agent binaries it embeds (see agent-binaries.sh),
# but a host that wants to run the agent without a manager, or a manager
# built from source, needs them on the release itself. The release
# workflow builds them with agent-binaries.sh and calls this with the tag
# and the dist directory.
#
# Assets keep the names the manager's dist uses, so a consumer sees one
# naming scheme everywhere:
#
#   pulsix-agent-<os>-<arch>          the binary (Windows without ".exe")
#   pulsix-agent-<os>-<arch>.sha256   "<hash>  <name>", from the same bytes
#
# A published asset is never replaced: someone may already have verified
# or mirrored it, and replacing bytes under a tag silently breaks every
# checksum taken before. So this is safe to re-run after a partial failure
# — identical bytes are skipped — and loud when a rebuild produced
# different ones (Go builds are reproducible with -trimpath, so a
# difference means the inputs changed and the tag needs a new release).
#
# Each binary goes up before its checksum, one file per call: a consumer
# that finds a checksum then also finds its binary, and a failure leaves
# a state a re-run completes.
#
# Usage: scripts/release-agent-assets.sh <tag> <dist-dir>
#   The repository comes from $GH_REPO, which gh itself reads.
#   The release must already exist (the release job creates it).
# POSIX sh, not bash: it runs wherever the release job does, and only
# needs gh, cmp and mktemp.
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: release-agent-assets.sh <tag> <dist-dir>" >&2
	exit 2
fi
tag="$1"
dist="$2"

die() {
	echo "release-agent-assets: $*" >&2
	exit 1
}

# Same shape the release workflow cuts: vX.Y.Z with an optional
# alpha/beta/rc pre-release suffix. Checked before anything is touched.
if ! printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$'; then
	die "tag '$tag' is not vX.Y.Z or vX.Y.Z-(alpha|beta|rc).N"
fi
[ -d "$dist" ] || die "dist directory '$dist' does not exist"

# Validate the whole dist before uploading anything, so a bad directory
# never leaves a half-attached release. The glob skips dotfiles, which is
# what keeps the committed .placeholder out.
binaries=0
for path in "$dist"/*; do
	[ -e "$path" ] || continue
	name="${path##*/}"
	if ! printf '%s\n' "$name" | grep -Eq '^pulsix-agent-(linux|windows|darwin)-(amd64|arm64)(\.sha256)?$'; then
		die "unexpected file in $dist: $name"
	fi
	case "$name" in
	*.sha256) [ -f "$dist/${name%.sha256}" ] || die "$name has no binary next to it" ;;
	*)
		[ -f "$dist/$name.sha256" ] || die "$name has no .sha256 next to it"
		binaries=$((binaries + 1))
		;;
	esac
done
[ "$binaries" -ge 1 ] || die "no agent binaries in $dist (run scripts/agent-binaries.sh first)"

# A failing view means there is no release for the tag (or gh cannot
# reach it): stop rather than let an upload create or guess at one.
existing="$(gh release view "$tag" --json assets --jq '.assets[].name')" ||
	die "cannot read release $tag (does it exist?)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# attach <name>: upload when absent, otherwise compare bytes.
attach() {
	name="$1"
	if printf '%s\n' "$existing" | grep -Fxq "$name"; then
		rm -f "$tmp/$name"
		gh release download "$tag" --pattern "$name" --dir "$tmp" ||
			die "cannot download $name from release $tag to compare it"
		if cmp -s "$dist/$name" "$tmp/$name"; then
			echo "release-agent-assets: $name already attached"
		else
			die "$name is already on release $tag with different bytes; refusing to replace it"
		fi
	else
		gh release upload "$tag" "$dist/$name" || die "cannot upload $name to release $tag"
		echo "release-agent-assets: $name attached"
	fi
}

# Binaries first, so a checksum never appears without its binary.
for path in "$dist"/pulsix-agent-*; do
	name="${path##*/}"
	case "$name" in *.sha256) continue ;; esac
	attach "$name"
done
for path in "$dist"/pulsix-agent-*.sha256; do
	attach "${path##*/}"
done
