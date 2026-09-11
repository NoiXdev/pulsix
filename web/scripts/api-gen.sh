#!/usr/bin/env bash
# Regenerates src/api/schema.d.ts from the manager's own OpenAPI document.
#
# The OpenAPI document is written to a temporary file scoped to this run,
# under web/'s own directory rather than a fixed shared path like
# /tmp/pulsix-openapi.json — a fixed path can collide between two
# concurrent invocations (two CI jobs, or a local run racing CI) and
# leaves stale content lying around if a previous run failed partway.
# `mktemp` gives each run its own file, and the trap below removes it
# whether generation succeeds or fails.
set -euo pipefail
cd "$(dirname "$0")/.."

tmp_dir=".tmp"
mkdir -p "$tmp_dir"
doc="$(mktemp "$tmp_dir/openapi.XXXXXX.json")"
trap 'rm -f "$doc"' EXIT

go run ../cmd/manager openapi > "$doc"
openapi-typescript "$doc" -o src/api/schema.d.ts
