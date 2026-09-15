#!/usr/bin/env bash
# coverage-gate.sh — per-package statement coverage gate.
#
# Policy (plan Global Constraints): every Go package must be at or above
# 80% line coverage; internal/auth, internal/tenancy, internal/db,
# internal/inventory/schema, internal/inventory/grouptree,
# internal/inventory/render, internal/credentials and internal/ca must be
# at or above 90% once those packages exist. A single blended number across
# the whole module cannot express two different floors and lets a weak
# package hide behind a strong one, so this checks every package
# individually.
#
# cmd/... is entry-point wiring (main.go, ListenAndServe) with no test
# files by design (see task-1 brief/report) and is explicitly excluded
# below — never silently, always printed as SKIP so the exclusion is
# visible in CI output.
#
# Machine-generated packages (sqlc, protoc, mockgen, ...) are excluded
# too, but the exclusion is EARNED BY EVIDENCE IN THE FILES, never
# granted by package path: a package counts as generated only when it
# has at least one non-test .go file and every non-test .go file in it
# carries a "Code generated ... DO NOT EDIT." header before its package
# clause, per Go's own convention (https://go.dev/s/generatedcode). Drop
# a single hand-written file into a generated directory without that
# header and the whole package falls back under the normal floor on the
# next run — see task-4-report.md "Fix round 2" for why internal/db/gen
# qualifies today and for the guard being exercised against a planted
# hand-written file.
#
# Usage: scripts/coverage-gate.sh
# Exit status: 0 if every non-excluded package meets its floor, 1 otherwise.

set -euo pipefail

module=$(go list -m)

# Find packages that are wholly machine-generated, by inspecting their own
# files rather than trusting their path. A package qualifies only if it has
# at least one non-test .go file and every such file has a
# "Code generated ... DO NOT EDIT." line somewhere before its package
# clause.
generated_pkgs=()
while IFS=$'\t' read -r import_path dir; do
	has_source_file=0
	all_generated=1
	for f in "$dir"/*.go; do
		[ -e "$f" ] || continue
		case "$f" in
			*_test.go) continue ;;
		esac
		has_source_file=1
		if ! sed -n '1,/^package /p' "$f" | grep -qE '^// Code generated .* DO NOT EDIT\.$'; then
			all_generated=0
			break
		fi
	done
	if [ "$has_source_file" -eq 1 ] && [ "$all_generated" -eq 1 ]; then
		generated_pkgs+=("$import_path")
	fi
done < <(go list -f $'{{.ImportPath}}\t{{.Dir}}' ./...)

generated_list=""
if [ "${#generated_pkgs[@]}" -gt 0 ]; then
	generated_list=$(printf '%s\n' "${generated_pkgs[@]}")
fi

go test -race -cover ./... 2>&1 | awk -v module="$module" -v generated="$generated_list" '
	BEGIN {
		FS = "\t"
		default_floor = 80
		fail = 0
		n = split(generated, gen_arr, "\n")
		for (i = 1; i <= n; i++) {
			if (gen_arr[i] != "") generated_set[gen_arr[i]] = 1
		}
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

		if (pkg in generated_set) {
			printf "SKIP  %-55s (machine-generated, DO NOT EDIT header on every file, excluded from gate)\n", pkg
			next
		}

		floor = default_floor
		if (pkg == module "/internal/auth" || pkg == module "/internal/tenancy" || pkg == module "/internal/db" ||
		    pkg == module "/internal/inventory/schema" || pkg == module "/internal/inventory/grouptree" ||
		    pkg == module "/internal/inventory/render" || pkg == module "/internal/credentials" ||
		    pkg == module "/internal/ca" || pkg == module "/internal/jobs" ||
		    pkg == module "/internal/worker/runner" || pkg == module "/internal/worker/sshprobe") {
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
