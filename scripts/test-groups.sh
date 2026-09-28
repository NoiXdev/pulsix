#!/usr/bin/env bash
# test-groups.sh — the race suite's package groups and their timeouts.
#
# Prints one line per group, "<name> <timeout> <import path>...", all
# space-separated, for `make test` and scripts/coverage-gate.sh to run.
# Space-separated rather than tab-separated so a caller needs no IFS of its
# own: plain `read -r name timeout pkgs` puts every remaining field in pkgs,
# and `go test $pkgs` splits it into arguments. It
# exists so the grouping is defined once instead of being copied into both,
# and so a new package cannot quietly fall outside every group: the
# partition is checked against `go list ./...` below and a miss is a hard
# error, not a package that silently stops being tested.
#
# Why groups at all, and why these three:
#
#   api  — internal/api alone runs 1005-1175 s under -race on an idle
#          machine and was measured at ~1264 s on a busy one (backlog 3.21,
#          three measurements 2026-09-28). `go test`'s -timeout applies to
#          each package's own binary, so one number for the whole module
#          has to be sized for this package and is then also what a hung
#          five-second package waits out. Splitting it off lets the slow
#          one have a real margin — 3600 s, about 3x its worst measurement
#          — while everything else fails fast.
#
#   e2e  — runs on its own invocation because a single whole-module
#          `go test -race ./...` intermittently hits internal/e2e's SSH
#          loopback timeouts on a loaded machine: every other package's
#          tests compete for CPU while the e2e fixtures wait on loopback
#          handshakes (backlog 2.83).
#
#   rest — everything else, at 900 s per package. The slowest of them
#          measured 147 s (internal/checks under -race -cover), so this is
#          six times the measured worst case and still short enough that a
#          deadlocked test names itself in fifteen minutes instead of an
#          hour.
#
# Usage: scripts/test-groups.sh
# Exit status: 0 when the groups partition `go list ./...` exactly.

set -euo pipefail

module=$(go list -m)

all=$(go list ./... | sort)

api_pkgs="$module/internal/api"
e2e_pkgs=$(printf '%s\n' "$all" | grep -E "^$module/internal/e2e(/|$)" || true)
# -F with a multi-line pattern is one fixed pattern per line, so this is
# "everything the two groups above did not already claim".
rest_pkgs=$(printf '%s\n' "$all" | grep -vxF "$(printf '%s\n%s\n' "$api_pkgs" "$e2e_pkgs")" || true)

# The partition check. `rest` is defined by subtraction, so a NEW package
# cannot fall outside the groups — what can go wrong is the other end: a
# hand-written path above that names a package which no longer exists
# (renamed, moved, split). Left unchecked that silently demotes the slow
# package back into `rest`'s tighter timeout, which is the whole thing
# this file exists to prevent, and nothing else would say a word.
covered=$(printf '%s\n%s\n%s\n' "$api_pkgs" "$e2e_pkgs" "$rest_pkgs" | grep -v '^$' | sort)
if [ "$covered" != "$all" ]; then
	echo "test-groups.sh: the groups do not partition 'go list ./...'" >&2
	echo "--- in no group, or in two:" >&2
	diff <(printf '%s\n' "$all") <(printf '%s\n' "$covered") >&2 || true
	exit 1
fi
if [ "$(printf '%s\n' "$covered" | uniq | wc -l)" -ne "$(printf '%s\n' "$covered" | wc -l)" ]; then
	echo "test-groups.sh: a package is in more than one group" >&2
	exit 1
fi

# The package list is space-separated because the callers feed it straight
# to `go test` as separate arguments. No trailing space: it would survive
# into the last argument under some shells' word splitting.
oneline() { printf '%s\n' "$1" | tr '\n' ' ' | sed 's/ *$//'; }

printf 'api 3600s %s\n' "$(oneline "$api_pkgs")"
printf 'e2e 1800s %s\n' "$(oneline "$e2e_pkgs")"
printf 'rest 900s %s\n' "$(oneline "$rest_pkgs")"
