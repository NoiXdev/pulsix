#!/usr/bin/env bash
# smoke.sh — runs the real manager and worker images through
# deploy/compose/docker-compose.yml and proves the basics work: the
# manager starts, answers /healthz, reports setup as not yet completed on
# a fresh database, and serves the SPA; after the setup wizard's API call
# the worker-enroll service enrolls the worker container, which holds
# neither the master key nor the database credentials, heartbeats and
# runs a job to its terminal state; the documented `worker add` CLI form
# prints a bundle; and a full stack restart registers no second worker. This is the last gate before an
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
#   PULSIX_SMOKE_PROJECT — the Compose project name the throwaway stack runs
#   under. Defaults to "pulsix-smoke".
#
# The stack always runs under its own Compose project name, never the
# default one: Compose derives that from the directory (deploy/compose ->
# "compose"), which is exactly what a real deployment started from
# deploy/compose uses too — and the teardown below runs `down -v`, which
# would otherwise stop that deployment and delete its database volume.
#
# Usage:
#   docker build -f deploy/docker/Dockerfile.manager -t noixdev/pulsix-manager:dev .
#   docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .
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
project="${PULSIX_SMOKE_PROJECT:-pulsix-smoke}"
if [ "$project" = "compose" ]; then
	echo "smoke: refusing to run under the Compose project name \"compose\" — that is the default a real deployment from deploy/compose uses" >&2
	exit 1
fi

for var in DB_PASSWORD PULSIX_MASTER_KEY PULSIX_PUBLIC_URL; do
	if [ -z "${!var:-}" ]; then
		echo "smoke: \$$var must be set (see deploy/compose/.env.example)" >&2
		exit 1
	fi
done
base_url="$PULSIX_PUBLIC_URL"

# -p always wins over COMPOSE_PROJECT_NAME and the directory default, so no
# environment an operator happens to have exported can point this script's
# `down -v` at their real deployment.
compose() {
	docker compose -p "$project" -f "$compose_file" "$@"
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

echo "smoke: starting the stack as Compose project \"$project\" (manager image tag: ${PULSIX_VERSION:-latest}) …"
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

# --- the worker: enrollment, isolation, a job, restart ----------------

# psql runs inside the db container as the schema owner (bypasses RLS —
# this is test bookkeeping, not an application path).
psql() {
	compose exec -T db psql -U pulsix -d pulsix -tAc "$1"
}

# wait_for <description> <timeout-seconds> <command...>: retry until the
# command succeeds.
wait_for() {
	local what="$1" timeout="$2"
	shift 2
	local deadline=$((SECONDS + timeout))
	until "$@"; do
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "smoke: timed out after ${timeout}s waiting for $what" >&2
			compose ps -a || true
			compose logs worker-enroll worker manager | tail -n 60 || true
			exit 1
		fi
		sleep 2
	done
}

worker_heartbeating() {
	[ "$(psql "SELECT count(*) FROM workers WHERE name = 'worker-1' AND last_seen > now() - interval '1 minute'")" = "1" ]
}

echo "smoke: completing the setup wizard through the API …"
setup_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
	-d '{"admin_email":"smoke@example.test","admin_password":"correct horse battery staple","admin_name":"Smoke","org_slug":"smoke","org_name":"Smoke"}' \
	"$base_url/api/v1/setup/complete")"
if [ "$setup_code" != "201" ] && [ "$setup_code" != "200" ]; then
	echo "smoke: setup/complete answered $setup_code" >&2
	exit 1
fi

echo "smoke: waiting for the worker container to enroll and heartbeat (timeout 180s) …"
wait_for "the enrolled worker's heartbeat" 180 worker_heartbeating
echo "smoke: worker enrolled and heartbeating"

echo "smoke: checking the worker container holds neither the master key nor the database credentials …"
worker_env="$(compose exec -T worker env)"
for forbidden in PULSIX_MASTER_KEY PULSIX_DATABASE_URL "$PULSIX_MASTER_KEY" "$DB_PASSWORD"; do
	if printf '%s' "$worker_env" | grep -qF -- "$forbidden"; then
		# Never echo the value itself: it may be a real secret.
		echo "smoke: the worker container's environment contains a manager secret or its variable" >&2
		exit 1
	fi
done
for forbidden in "$PULSIX_MASTER_KEY" "$DB_PASSWORD"; do
	if compose exec -T worker grep -rqF -- "$forbidden" /etc/pulsix-worker /var/lib/pulsix-worker 2>/dev/null; then
		echo "smoke: a file in the worker container contains a manager secret" >&2
		exit 1
	fi
done
echo "smoke: worker environment variable names: $(printf '%s\n' "$worker_env" | cut -d= -f1 | grep '^PULSIX_' | sort | tr '\n' ' ')"
echo "smoke: worker environment and files are free of manager secrets"

echo "smoke: enqueueing a repository sync against an unreachable remote …"
org_id="$(psql "SELECT id FROM organizations WHERE slug = 'smoke'")"
zone_id="$(psql "SELECT id FROM zones WHERE org_id = '$org_id' AND slug = 'default'")"
repo_id="$(psql "SELECT gen_random_uuid()")"
job_id="$(psql "SELECT gen_random_uuid()")"
psql "INSERT INTO git_repos (id, org_id, name, url) VALUES ('$repo_id', '$org_id', 'smoke', 'https://127.0.0.1:9/unreachable.git')" >/dev/null
psql "INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id, payload) VALUES ('$job_id', '$org_id', '$zone_id', 'sync_repo', gen_random_uuid(), '{\"repo_id\":\"$repo_id\"}')" >/dev/null
job_finished() {
	case "$(psql "SELECT status FROM jobs WHERE id = '$job_id'")" in
	succeeded | failed | error) return 0 ;;
	*) return 1 ;;
	esac
}
wait_for "the sync job to finish" 120 job_finished
job_status="$(psql "SELECT status FROM jobs WHERE id = '$job_id'")"
sync_error="$(psql "SELECT coalesce(last_sync_error, '') FROM git_repos WHERE id = '$repo_id'")"
if [ "$job_status" = "succeeded" ] || [ -z "$sync_error" ]; then
	echo "smoke: sync job ended $job_status with last_sync_error '$sync_error', want a recorded failure" >&2
	exit 1
fi
echo "smoke: the worker claimed and finished the job (status $job_status, sync error recorded)"

echo "smoke: checking the documented enrollment command prints a bundle …"
cli_out="$(compose run --rm --no-deps manager worker add --zone default --name smoke-cli)"
case "$cli_out" in
*'# client certificate'*'# private key'*'# CA certificate'*'PULSIX_MANAGER_SERVER_NAME='*) ;;
*)
	echo "smoke: worker add printed no bundle: $cli_out" >&2
	exit 1
	;;
esac
echo "smoke: worker add CLI ok"

echo "smoke: restarting the whole stack …"
compose down
compose up -d
wait_for "/healthz after the restart" 90 curl -fsS -o /dev/null "$base_url/healthz"
enroll_exited_ok() {
	[ "$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$(compose ps -a -q worker-enroll)" 2>/dev/null)" = "exited 0" ]
}
wait_for "the enrollment service to finish after the restart" 120 enroll_exited_ok
wait_for "the worker's heartbeat after the restart" 120 worker_heartbeating
rows="$(psql "SELECT count(*) FROM workers WHERE name = 'worker-1'")"
if [ "$rows" != "1" ]; then
	echo "smoke: $rows worker rows named worker-1 after the restart, want 1" >&2
	exit 1
fi
echo "smoke: enrollment service log after the restart: $(compose logs --no-log-prefix worker-enroll 2>&1 | grep 'worker add:' | tail -n 1)"
echo "smoke: restart reused the enrollment (one worker row)"

echo "smoke: ok"
