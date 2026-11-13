#!/bin/sh
# install-smoke-macos.sh — CI's install smoke for the macOS agent.
#
# Runs ONLY on a disposable CI runner (the agent-macos job in
# .github/workflows/ci.yml): it installs the agent as a system launchd
# daemon with sudo, writes to /usr/local/bin and /Library, and removes it
# all again at the end. Never run it on a machine you care about.
#
# The installer is the real one, fetched from installstub
# (internal/agentdist/cmd/installstub), which must already be listening at
# the base URL with the runner's own binary in its dist. The install points
# the agent at manager 127.0.0.1:1, where nothing listens: the agent keeps
# retrying its enrollment, which is enough to show that it runs as a
# daemon. It installs twice, a fresh install and then an upgrade over the
# running agent, and checks the daemon, the file protection and the log
# after each. Then it removes the agent with the installer's own
# --uninstall and checks that daemon, plist, binary, logs and data
# directory are gone, and runs the uninstall again, which finds nothing
# and still succeeds. The cleanup trap removes the agent by hand as a
# fallback, whatever step failed.
#
# Usage: scripts/ci/install-smoke-macos.sh [base URL]  (default http://127.0.0.1:18080)

set -eu

BASE_URL="${1:-http://127.0.0.1:18080}"
LABEL="dev.noix.pulsix-agent"
BIN="/usr/local/bin/pulsix-agent"
DATA_DIR="/Library/Application Support/Pulsix"
ENV_FILE="$DATA_DIR/agent.env"
PLIST="/Library/LaunchDaemons/$LABEL.plist"
LOG_DIR="/Library/Logs/Pulsix"
LOG_FILE="$LOG_DIR/agent.log"
# Well-formed (enrolltoken.ParseAgent accepts it) but unknown to any
# manager: a malformed one would make the agent exit at once.
TOKEN="pxa1.ASNFZ4mrze8BI0VniavN7w.ERERERERERERERERERERERERERERERERERERERERERE.IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI"

WORK="$(mktemp -d)"
TOKEN_FILE="$WORK/token"

cleanup() {
	echo "install-smoke: removing the agent"
	sudo launchctl bootout "system/$LABEL" 2> /dev/null || true
	sudo rm -f "$PLIST" "$BIN"
	sudo rm -rf "$DATA_DIR" "$LOG_DIR" "$WORK"
}
trap cleanup EXIT

diagnostics() {
	echo "--- launchctl print system/$LABEL"
	sudo launchctl print "system/$LABEL" || true
	echo "--- $LOG_FILE"
	sudo cat "$LOG_FILE" || true
}

fail() {
	echo "install-smoke: FAIL: $1" >&2
	diagnostics >&2
	exit 1
}

# The token file the installer accepts: root's, mode 0600.
printf '%s\n' "$TOKEN" > "$TOKEN_FILE"
sudo chown 0:0 "$TOKEN_FILE"
sudo chmod 600 "$TOKEN_FILE"

# owner_mode PATH: "<owner name> <octal mode>".
owner_mode() {
	sudo stat -f '%Su %Lp' "$1"
}

# starts_logged: how many times the agent has logged its start.
starts_logged() {
	sudo grep -c 'pulsix-agent starting' "$LOG_FILE" 2> /dev/null || true
}

# assert_installed N: the daemon runs and has logged exactly N starts, and
# every file the installer wrote carries its protection. Any start beyond N
# means the agent exited and launchd started it again, which a running
# daemon alone would hide.
assert_installed() {
	want_starts="$1"
	tries=0
	while :; do
		running=no
		if sudo launchctl print "system/$LABEL" 2> /dev/null | grep -q 'state = running'; then
			running=yes
		fi
		starts="$(starts_logged)"
		[ -n "$starts" ] || starts=0
		[ "$starts" -le "$want_starts" ] ||
			fail "'pulsix-agent starting' logged $starts times, want exactly $want_starts: the agent exited and was started again"
		if [ "$running" = yes ] && [ "$starts" -eq "$want_starts" ]; then
			break
		fi
		tries=$((tries + 1))
		[ "$tries" -le 30 ] || fail "after 30 s: running=$running, 'pulsix-agent starting' logged $starts times (want exactly $want_starts)"
		sleep 1
	done
	# Longer than the plist's ThrottleInterval (5 s), so an agent that
	# exits and is restarted shows up as a second start within it.
	sleep 6
	sudo launchctl print "system/$LABEL" 2> /dev/null | grep -q 'state = running' ||
		fail "the daemon did not stay running"
	after="$(starts_logged)"
	[ "$after" = "$starts" ] || fail "the agent restarted: the start count went from $starts to $after"

	[ -x "$BIN" ] || fail "$BIN is missing or not executable"
	[ -f "$PLIST" ] || fail "$PLIST is missing"
	got="$(owner_mode "$DATA_DIR")"
	[ "$got" = "root 700" ] || fail "$DATA_DIR is '$got', want 'root 700'"
	got="$(owner_mode "$ENV_FILE")"
	[ "$got" = "root 600" ] || fail "$ENV_FILE is '$got', want 'root 600'"
	got="$(owner_mode "$PLIST")"
	[ "$got" = "root 644" ] || fail "$PLIST is '$got', want 'root 644'"
	sudo test -f "$LOG_FILE" || fail "$LOG_FILE is missing"
	sudo grep -qx 'PULSIX_MANAGER_ADDR=127.0.0.1:1' "$ENV_FILE" || fail "$ENV_FILE does not name the manager"
	echo "install-smoke: install $want_starts OK (running, protected, $starts start(s) logged)"
}

# fetch_installer: the script is downloaded first rather than piped, so a
# failed download fails here instead of feeding sh an empty script.
fetch_installer() {
	curl -fsSL -o "$WORK/install.sh" "$BASE_URL/install.sh" || fail "downloading $BASE_URL/install.sh failed"
}

# install_agent: the documented `sh -s --` invocation, with the token from
# the file.
install_agent() {
	fetch_installer
	# The redirect is the runner user's on purpose: it reads its own download.
	# shellcheck disable=SC2024
	sudo sh -s -- --manager 127.0.0.1:1 --name ci-smoke --token-file "$TOKEN_FILE" < "$WORK/install.sh" ||
		fail "install.sh failed"
}

# uninstall_agent: the documented `sh -s -- --uninstall` invocation. Its
# output is echoed and kept in UNINSTALL_OUT.
uninstall_agent() {
	fetch_installer
	# shellcheck disable=SC2024
	UNINSTALL_OUT="$(sudo sh -s -- --uninstall < "$WORK/install.sh")" ||
		fail "install.sh --uninstall failed: $UNINSTALL_OUT"
	printf '%s\n' "$UNINSTALL_OUT"
}

# assert_output TEXT: the last uninstall printed the line TEXT.
assert_output() {
	printf '%s\n' "$UNINSTALL_OUT" | grep -qxF "$1" || fail "the uninstall did not print '$1'"
}

echo "install-smoke: fresh install"
install_agent
assert_installed 1

echo "install-smoke: upgrade over the running agent"
previous_starts="$(starts_logged)"
install_agent
assert_installed $((previous_starts + 1))

echo "install-smoke: uninstall"
uninstall_agent
assert_output "pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page"
if sudo launchctl print "system/$LABEL" > /dev/null 2>&1; then
	fail "launchd still knows system/$LABEL after the uninstall"
fi
for path in "$PLIST" "$BIN" "$LOG_DIR" "$DATA_DIR"; do
	sudo test ! -e "$path" || fail "$path is still there after the uninstall"
done

echo "install-smoke: uninstall again, with nothing installed"
uninstall_agent
assert_output "pulsix-agent: nothing to remove"

echo "install-smoke: PASS"
