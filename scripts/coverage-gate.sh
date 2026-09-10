#!/usr/bin/env bash
# coverage-gate.sh — per-package statement coverage gate.
#
# Policy (plan Global Constraints): every Go package must be at or above
# 80% line coverage; internal/auth, internal/tenancy and internal/db must
# be at or above 90% once those packages exist. A single blended number
# across the whole module cannot express two different floors and lets a
# weak package hide behind a strong one, so this checks every package
# individually.
#
# cmd/... is entry-point wiring (main.go, ListenAndServe) with no test
# files by design (see task-1 brief/report) and is explicitly excluded
# below — never silently, always printed as SKIP so the exclusion is
# visible in CI output.
#
# internal/db/gen is sqlc-generated code (see task-4 brief/report): it has
# no hand-written logic to cover and is exercised indirectly by
# internal/db's own tests, so it is excluded the same way and for the same
# reason — never silently, always printed as SKIP.
#
# Usage: scripts/coverage-gate.sh
# Exit status: 0 if every non-excluded package meets its floor, 1 otherwise.

set -euo pipefail

module=$(go list -m)

go test -race -cover ./... 2>&1 | awk -v module="$module" '
	BEGIN {
		FS = "\t"
		default_floor = 80
		fail = 0
	}

	# Only lines shaped like "<result>\t<package>\t<duration>\t<coverage>"
	# (or the no-test-files variant, whose result field is empty) carry a
	# package name in field 2; anything else (race notes, build errors,
	# already surfaced by the earlier lint/test steps) is ignored here.
	NF < 2 || $2 !~ ("^" module "/") { next }

	{
		pkg = $2

		if (pkg ~ ("^" module "/cmd(/|$)")) {
			printf "SKIP  %-55s (cmd/... entry-point wiring, excluded from gate)\n", pkg
			next
		}

		if (pkg == module "/internal/db/gen") {
			printf "SKIP  %-55s (sqlc-generated code, excluded from gate)\n", pkg
			next
		}

		floor = default_floor
		if (pkg == module "/internal/auth" || pkg == module "/internal/tenancy" || pkg == module "/internal/db") {
			floor = 90
		}

		pct = 0
		if (match($0, /[0-9]+\.[0-9]+% of statements/)) {
			pct = substr($0, RSTART, RLENGTH) + 0
		}

		if (pct >= floor) {
			printf "PASS  %-55s %6.1f%%  (floor %d%%)\n", pkg, pct, floor
		} else {
			printf "FAIL  %-55s %6.1f%%  (floor %d%%)\n", pkg, pct, floor
			fail = 1
		}
	}

	END {
		if (fail) {
			print "coverage gate: one or more packages below their required floor"
			exit 1
		}
	}
'
