#!/usr/bin/env bash
# release-agent-assets-test.sh — proves scripts/release-agent-assets.sh
# attaches the agent binaries to a release the way the release workflow
# relies on: a fresh release gets everything, a re-run after a partial
# failure only adds what is missing, and a published asset is never
# replaced — identical bytes are skipped, different bytes fail loudly.
#
# It runs against a fake `gh` that serves a "release" from a directory
# ($FAKE_RELEASE; absent = the tag has no release), so it needs neither
# the network nor credentials. The fake refuses to upload a name that
# already exists, as the real gh does without --clobber.
#
# Usage: scripts/release-agent-assets-test.sh

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/release-agent-assets.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir "$work/bin"
cat > "$work/bin/gh" <<'FAKE'
#!/bin/sh
# Records every call in $FAKE_LOG, then behaves like the three gh
# subcommands the script uses.
set -eu
echo "$*" >> "$FAKE_LOG"
[ "$1 $2" = "release view" ] || [ "$1 $2" = "release download" ] || [ "$1 $2" = "release upload" ] || exit 2
cmd="$2"
shift 3 # release <cmd> <tag>
[ -d "${FAKE_RELEASE:-}" ] || { echo "release not found" >&2; exit 1; }
case "$cmd" in
view)
	ls "$FAKE_RELEASE"
	;;
download)
	[ "$1" = "--pattern" ] && [ "$3" = "--dir" ] || exit 2
	cp "$FAKE_RELEASE/$2" "$4/$2"
	;;
upload)
	name="$(basename "$1")"
	[ ! -e "$FAKE_RELEASE/$name" ] || { echo "asset under the same name already exists: $name" >&2; exit 1; }
	cp "$1" "$FAKE_RELEASE/$name"
	;;
esac
FAKE
chmod +x "$work/bin/gh"

export PATH="$work/bin:$PATH"
export FAKE_LOG="$work/gh.log"
export FAKE_RELEASE="$work/release"

fail=0
bad() {
	echo "release-agent-assets-test: $case_name: $*" >&2
	fail=1
}

# Each case starts from a clean release dir, dist dir and call log.
# setup [with-release]
setup() {
	rm -rf "$work/release" "$work/dist" "$FAKE_LOG"
	mkdir "$work/dist"
	: > "$FAKE_LOG"
	[ "${1:-}" = "with-release" ] && mkdir "$work/release"
	return 0
}

dist_pair() { # <name> <binary-bytes>
	printf '%s' "$2" > "$work/dist/$1"
	printf 'hash-of-%s  %s\n' "$2" "$1" > "$work/dist/$1.sha256"
}

# run_script <tag>: stores status in $status and output in $out.
run_script() {
	status=0
	out="$("$script" "$1" "$work/dist" 2>&1)" || status=$?
}

want_fail() {
	# 126/127 mean the script could not even start: not a rejection.
	[ "$status" -ne 126 ] && [ "$status" -ne 127 ] || bad "script did not run: $out"
	[ "$status" -ne 0 ] || bad "expected a non-zero exit, got 0: $out"
}
want_ok() {
	[ "$status" -eq 0 ] || bad "expected exit 0, got $status: $out"
}
want_same() { # <name>: release holds the same bytes as dist
	cmp -s "$work/dist/$1" "$work/release/$1" || bad "$1 missing or different on the release"
}

tag=v0.1.0
bin=pulsix-agent-linux-amd64

case_name=no_release
setup
run_script "$tag"
want_fail
[ ! -e "$work/release" ] || bad "created a release"

case_name=fresh_upload
setup with-release
dist_pair "$bin" "payload"
run_script "$tag"
want_ok
want_same "$bin"
want_same "$bin.sha256"

case_name=partial_rerun
setup with-release
dist_pair "$bin" "payload"
cp "$work/dist/$bin" "$work/release/$bin"
run_script "$tag"
want_ok
want_same "$bin.sha256"
if grep -q "^release upload .*$bin\$" "$FAKE_LOG"; then bad "re-uploaded the binary"; fi

case_name=differing_asset
setup with-release
dist_pair "$bin" "payload"
printf 'other' > "$work/release/$bin"
run_script "$tag"
want_fail
case "$out" in *"$bin"*) ;; *) bad "message does not name $bin: $out" ;; esac
[ "$(cat "$work/release/$bin")" = other ] || bad "replaced the published binary"
[ "$(ls "$work/release")" = "$bin" ] || bad "release changed"

case_name=empty_dist
setup with-release
: > "$work/dist/.placeholder"
run_script "$tag"
want_fail

case_name=bad_tag
setup with-release
dist_pair "$bin" "payload"
run_script 0.1.0
want_fail
[ ! -s "$FAKE_LOG" ] || bad "called gh before rejecting the tag"

case_name=foreign_file
setup with-release
dist_pair "$bin" "payload"
echo hi > "$work/dist/notes.txt"
run_script "$tag"
want_fail
[ -z "$(ls "$work/release")" ] || bad "uploaded despite a foreign file"

case_name=missing_checksum
setup with-release
dist_pair "$bin" "payload"
rm "$work/dist/$bin.sha256"
run_script "$tag"
want_fail
[ -z "$(ls "$work/release")" ] || bad "uploaded despite a missing checksum"

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "release-agent-assets-test: all cases ok"
