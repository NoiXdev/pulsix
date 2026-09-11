#!/usr/bin/env bash
# smoke.sh — runs the real manager image through
# deploy/compose/docker-compose.yml and proves the basics work: the
# manager starts, answers /healthz, reports setup as not yet completed on
# a fresh database, and serves the SPA. This is the last gate before an
# image is trusted — the CI "docker" job runs this on every push, and the
# release workflow depends on that job passing before it ever builds and
# pushes a tagged image.
#
# Required environment (no .env file is read — these must already be
# exported; see deploy/compose/.env.example for what each one means):
#   DB_PASSWORD, PULSIX_MASTER_KEY, PULSIX_PUBLIC_URL
# Optional:
#   PULSIX_VERSION — image tag to run, e.g. "dev" for a just-built local
#   image. Defaults to "latest", same as the compose file itself.
#
# Usage:
#   docker build -f deploy/docker/Dockerfile.manager -t noixdev/pulsix-manager:dev .
#   PULSIX_VERSION=dev DB_PASSWORD=dev PULSIX_MASTER_KEY=$(openssl rand -hex 32) \
#     PULSIX_PUBLIC_URL=http://localhost:8080 scripts/smoke.sh
#
# Exit status: 0 and a final "smoke: ok" line on success, non-zero
# otherwise. The stack is torn down (docker compose down -v) whether this
# succeeds or fails — see the trap below — so a failed run never leaves a
# container or volume behind for the next one.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$repo_root/deploy/compose/docker-compose.yml"

for var in DB_PASSWORD PULSIX_MASTER_KEY PULSIX_PUBLIC_URL; do
	if [ -z "${!var:-}" ]; then
		echo "smoke: \$$var must be set (see deploy/compose/.env.example)" >&2
		exit 1
	fi
done
base_url="$PULSIX_PUBLIC_URL"

compose() {
	docker compose -f "$compose_file" "$@"
}

# Always torn down, whether the checks below pass or fail: the trap fires
# on any exit (success, a failed check's `exit 1`, or `set -e` killing the
# script on an unexpected error alike), and preserves whatever exit status
# triggered it rather than always exiting 0.
cleanup() {
	local status=$?
	echo "smoke: tearing down …"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT

echo "smoke: starting the stack (manager image tag: ${PULSIX_VERSION:-latest}) …"
compose up -d

echo "smoke: waiting for GET /healthz to answer 200 (timeout 90s) …"
deadline=$((SECONDS + 90))
until curl -fsS -o /dev/null "$base_url/healthz"; do
	if [ "$SECONDS" -ge "$deadline" ]; then
		echo "smoke: timed out after 90s waiting for /healthz" >&2
		compose logs manager || true
		exit 1
	fi
	sleep 1
done
echo "smoke: /healthz answered 200"

echo "smoke: checking GET /api/v1/setup/status reports completed:false …"
status_body="$(curl -fsS "$base_url/api/v1/setup/status")"
case "$status_body" in
*'"completed":false'*) ;;
*)
	echo "smoke: setup/status = $status_body, want a body containing \"completed\":false" >&2
	exit 1
	;;
esac

echo "smoke: checking GET / serves the SPA …"
root_body="$(curl -fsS "$base_url/")"
case "$root_body" in
*'<title>Pulsix'*) ;;
*)
	echo "smoke: GET / did not contain <title>Pulsix — got: $root_body" >&2
	exit 1
	;;
esac

echo "smoke: ok"
