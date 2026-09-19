#!/usr/bin/env bash
# worker-image-test.sh — proves the built worker image can actually use
# ssh as its runtime user. OpenSSH looks the running uid up in the passwd
# database before it does anything else ("No user exists for uid …" when
# it is missing), so `ssh -V` proves nothing: every check here makes ssh,
# git over ssh and Ansible open a connection to a closed local port and
# asserts they got as far as the connection attempt. The v0.2b image ran
# as a uid without a passwd entry and could not reach any SSH host
# (v0.2c final re-review, out-of-scope 1).
#
# Usage: scripts/worker-image-test.sh [image]   (default noixdev/pulsix-worker:dev)
# scripts/smoke.sh runs it against the image under test before the stack.

set -euo pipefail

image="${1:-noixdev/pulsix-worker:${PULSIX_VERSION:-dev}}"

# in_image <command...>: run a command in a throwaway container as the
# image's own user, with no network (the connection attempt goes to a
# closed port on the container's loopback), and print its combined output.
in_image() {
	docker run --rm --network none --entrypoint "$1" "$image" "${@:2}" 2>&1 || true
}

fail=0
# expect_connection_attempt <what> <output>: the output must show a
# connection attempt and must not show the missing-user error.
expect_connection_attempt() {
	local what="$1" out="$2"
	if printf '%s' "$out" | grep -q "No user exists"; then
		echo "worker-image-test: $what: the runtime user has no passwd entry: $out" >&2
		fail=1
	elif ! printf '%s' "$out" | grep -qi "connection refused"; then
		echo "worker-image-test: $what: expected a refused connection, got: $out" >&2
		fail=1
	else
		echo "worker-image-test: $what: ok"
	fi
}

echo "worker-image-test: checking $image …"

user="$(in_image id -un)"
# shellcheck disable=SC2016 # expands inside the container
home="$(in_image sh -c 'getent passwd "$(id -u)" | cut -d: -f6')"
if [ "$user" != "pulsix" ] || [ -z "$home" ] || [ "$home" = "/" ]; then
	echo "worker-image-test: runtime user = '$user', home = '$home', want pulsix with its own home" >&2
	fail=1
else
	echo "worker-image-test: runtime user $user, home $home: ok"
fi

expect_connection_attempt "ssh" \
	"$(in_image ssh -o BatchMode=yes -o ConnectTimeout=2 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null x@127.0.0.1 true)"

expect_connection_attempt "git over ssh" \
	"$(in_image env GIT_SSH_COMMAND='ssh -o BatchMode=yes -o ConnectTimeout=2 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' \
		git ls-remote ssh://x@127.0.0.1/repo.git)"

expect_connection_attempt "ansible over ssh" \
	"$(in_image env ANSIBLE_HOST_KEY_CHECKING=False ANSIBLE_NOCOLOR=1 \
		ansible all -i 127.0.0.1, -u x -m ansible.builtin.ping -T 2)"

# The worker's extra-vars file carries a key longer than YAML's 1024-
# character implicit-key limit (runner.extraVarsGuardVar), so a failed JSON
# load of the file — e.g. nesting past Python's recursion limit — fails the
# run instead of silently re-reading it as YAML, which would drop every
# unsafe marker. Check that premise against the image's own ansible-core
# and YAML parser, with a 300-deep value next to a marked lookup.
# shellcheck disable=SC2016 # Python source, nothing for the shell to expand
guard_out="$(in_image python -c '
import json
from ansible.parsing.utils.yaml import from_yaml
deep = "leaf"
for _ in range(300):
    deep = [deep]
doc = {"branch": {"__ansible_unsafe": "{{ lookup(\"pipe\", \"id\") }}"}, "deep": deep}
try:
    from_yaml(json.dumps(doc))
    print("without guard: falls back to YAML")
except Exception:
    print("without guard: fails")
doc["pulsix_extravars_guard"] = {"k" * 1100: True}
try:
    from_yaml(json.dumps(doc))
    print("guard: LOADED")
except Exception:
    print("guard: refused")
')"
if printf '%s' "$guard_out" | grep -q "guard: refused"; then
	echo "worker-image-test: extra-vars YAML guard: ok ($(printf '%s' "$guard_out" | head -n 1))"
else
	echo "worker-image-test: extra-vars YAML guard: the image's Ansible loaded a guarded document: $guard_out" >&2
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	echo "worker-image-test: FAILED" >&2
	exit 1
fi
echo "worker-image-test: ok"
