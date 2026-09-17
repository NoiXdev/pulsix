#!/usr/bin/env bash
# check-conventional-commits.sh — fail when a commit subject in the given
# revision range does not follow Conventional Commits.
#
# Replaces webiny/action-conventional-commits, whose newest release (v1.3.0)
# still targets the deprecated Node 20 runtime. Same contract, checked from
# the local clone instead of the GitHub API:
#   <type>[(<scope>)][!]: <subject>
# with the types the repository uses (CLAUDE.md) plus style and revert.
# Merge commits are skipped; git's own "Revert ..." subjects are accepted.
#
# Usage: scripts/check-conventional-commits.sh <git-log revision args...>
#   e.g. scripts/check-conventional-commits.sh origin/main..HEAD
# Exit status: 0 if every non-merge commit conforms, 1 otherwise.

set -euo pipefail

if [ "$#" -eq 0 ]; then
	echo "usage: $0 <git-log revision args...>" >&2
	exit 2
fi

types='feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert'
pattern="^(${types})(\\([^()[:space:]]+\\))?!?: [^[:space:]]"

checked=0
bad=0
while IFS=$'\t' read -r sha subject; do
	[ -n "$sha" ] || continue
	checked=$((checked + 1))
	if [[ "$subject" =~ $pattern ]] || [[ "$subject" == Revert\ * ]]; then
		printf 'ok    %s %s\n' "${sha:0:12}" "$subject"
	else
		printf 'FAIL  %s %s\n' "${sha:0:12}" "$subject"
		bad=1
	fi
done < <(git log --no-merges --format=$'%H\t%s' "$@")

if [ "$bad" -ne 0 ]; then
	echo "conventional commits: one or more commit subjects do not follow <type>[(scope)][!]: <subject> (types: ${types//|/, })"
	exit 1
fi
echo "conventional commits: ${checked} commit(s) checked"
