#!/usr/bin/env bash
# coverage-gate.sh — per-package statement coverage gate.
#
# Policy (plan Global Constraints): every Go package must be at or above
# 80% line coverage; internal/auth, internal/tenancy, internal/db,
# internal/inventory/schema, internal/inventory/grouptree,
# internal/inventory/render, internal/credentials, internal/ca and
# internal/outbound must be at or above 90% once those packages exist. A
# single blended number across the whole module cannot express two
# different floors and lets a weak package hide behind a strong one, so
# this checks every package individually.
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
# hand-written file. The one allowance: a hand-written file that declares
# no function at all (the word "func" never appears in it — constants and
# types only, like internal/pb/workerpb/limits.go) carries no coverable
# statement, so it cannot hide untested code and does not disqualify an
# otherwise generated package. At least one generated file is still
# required.
#
# A package whose coverage line reads "[no statements]" (test files only,
# e.g. internal/pb, whose tests exercise the generated workerpb) has
# nothing to measure and is printed as SKIP too.
#
# A package whose tests FAIL is reported as such (not as "0.0%"), and the
# go test output of every failing package is printed after the table, so
# a CI log shows why — the table alone hides it.
#
# Portable across awk implementations (GNU awk, mawk, BSD awk on macOS):
# no multi-line -v assignments, no gawk-only functions.
#
# Usage: scripts/coverage-gate.sh
#   COVERAGE_GATE_GO_TEST_OUTPUT=<file> evaluates a saved
#   `go test -cover ./...` output instead of running the tests (used to
#   check the gate's own parsing without a full test run).
# Exit status: 0 if every non-excluded package meets its floor and go test
# succeeded, 1 otherwise.

set -euo pipefail

module=$(go list -m)

# Find packages that are wholly machine-generated, by inspecting their own
# files rather than trusting their path. A package qualifies only if it has
# at least one non-test .go file and every such file has a
# "Code generated ... DO NOT EDIT." line somewhere before its package
# clause.
generated_pkgs=()
while IFS=$'\t' read -r import_path dir; do
	has_generated_file=0
	all_generated=1
	for f in "$dir"/*.go; do
		[ -e "$f" ] || continue
		case "$f" in
			*_test.go) continue ;;
		esac
		if sed -n '1,/^package /p' "$f" | grep -qE '^// Code generated .* DO NOT EDIT\.$'; then
			has_generated_file=1
		elif grep -qw 'func' "$f"; then
			all_generated=0
			break
		fi
	done
	if [ "$has_generated_file" -eq 1 ] && [ "$all_generated" -eq 1 ]; then
		generated_pkgs+=("$import_path")
	fi
done < <(go list -f $'{{.ImportPath}}\t{{.Dir}}' ./...)

# Space-separated: import paths never contain spaces, and BSD awk rejects
# a -v value that spans lines ("newline in string").
generated_list="${generated_pkgs[*]:-}"

test_output=$(mktemp)
trap 'rm -f "$test_output"' EXIT
test_rc=0

# -timeout 900s (Task 17): go test's per-package default is 600s, and
# internal/api -race alone exceeds it on some machines (its ~190 handler
# tests each build a full apitest.New app against the shared testcontainer
# database) — a package hitting the default timeout panics the whole test
# binary instead of failing cleanly, which would abort this gate and every
# `go test -race ./...` invocation (Makefile's `test` target does the
# same). 900s is a generous margin over the slowest observed run, not a
# tuned minimum.
if [ -n "${COVERAGE_GATE_GO_TEST_OUTPUT:-}" ]; then
	cp "$COVERAGE_GATE_GO_TEST_OUTPUT" "$test_output"
else
	go test -race -cover -timeout 900s ./... >"$test_output" 2>&1 || test_rc=$?
fi

gate_rc=0
awk -v module="$module" -v generated="$generated_list" '
	BEGIN {
		FS = "\t"
		default_floor = 80
		fail = 0
		n = split(generated, gen_arr, " ")
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
			printf "SKIP  %-55s (machine-generated: DO NOT EDIT header on every file with code, excluded from gate)\n", pkg
			next
		}

		# go test prints "FAIL\t<pkg>\t<duration>" (or "[build failed]" /
		# "[setup failed]") with no coverage figure for a failing package.
		if ($1 ~ /^FAIL/) {
			printf "FAIL  %-55s tests failed (output below)\n", pkg
			fail = 1
			next
		}

		if ($0 ~ /coverage: \[no statements\]/) {
			printf "SKIP  %-55s (no statements to cover, excluded from gate)\n", pkg
			next
		}

		floor = default_floor
		if (pkg == module "/internal/auth" || pkg == module "/internal/tenancy" || pkg == module "/internal/db" ||
		    pkg == module "/internal/inventory/schema" || pkg == module "/internal/inventory/grouptree" ||
		    pkg == module "/internal/inventory/render" || pkg == module "/internal/credentials" ||
		    pkg == module "/internal/ca" || pkg == module "/internal/jobs" ||
		    pkg == module "/internal/worker/runner" || pkg == module "/internal/worker/sshprobe" ||
		    pkg == module "/internal/clientip" || pkg == module "/internal/cronexpr" ||
		    pkg == module "/internal/enrolltoken" || pkg == module "/internal/outbound" ||
		    pkg == module "/internal/maintenance" || pkg == module "/internal/calendar") {
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
			print "coverage gate: one or more packages failed or are below their required floor"
			exit 1
		}
	}
' "$test_output" || gate_rc=$?

if [ "$test_rc" -ne 0 ] || [ "$gate_rc" -ne 0 ]; then
	# Passing packages print only their "ok" line (a package without tests
	# a "?" or a tab-led "coverage:" line), so everything else is the
	# failing packages' own output (test failures, panics, build errors).
	echo
	echo "==== go test output (passing packages omitted) ===="
	grep -vE -e $'^ok  \t' -e $'^[?]   \t' -e $'^\t[^\t]+\t+coverage: ' "$test_output" || true
	if [ "$gate_rc" -eq 0 ]; then
		echo "coverage gate: go test exited with status $test_rc"
	fi
	exit 1
fi
