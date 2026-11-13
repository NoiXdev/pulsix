#!/bin/sh
# install-smoke-linux.sh — CI's install smoke for the Linux agent.
#
# Runs ONLY on a disposable CI runner (the agent-linux job in
# .github/workflows/ci.yml): it installs the agent as a systemd service
# with sudo, writes to /usr/local/bin, /etc/pulsix and /etc/systemd, and
# removes it all again at the end. Never run it on a machine you care
# about.
#
# The installer is the real one, fetched from installstub
# (internal/agentdist/cmd/installstub), which must already be listening at
# the base URL with the runner's own binary in its dist. The install points
# the agent at manager 127.0.0.1:1, where nothing listens: the agent keeps
# retrying its enrollment, which is enough to show that it runs as a
# service. The sequence:
#
#   1. a fresh install, and a check that the unit's address-family filter
#      lets a process read the network interfaces over netlink;
#   2. an upgrade over the running agent, which must restart it: exactly
#      one new start in the journal (backlog 3.49);
#   3. an uninstall with --keep-data, which removes the unit and the
#      binary but keeps /etc/pulsix;
#   4. a reinstall over the kept data directory;
#   5. a full uninstall, after which unit, binary and /etc/pulsix are gone
#      and systemd no longer knows the unit;
#   6. the uninstall again, which finds nothing and still succeeds.
#
# Every install is checked for the running service, the start count and
# the file protection. The cleanup trap removes the agent by hand as a
# fallback, whatever step failed.
#
# Usage: scripts/ci/install-smoke-linux.sh [base URL]  (default http://127.0.0.1:18080)

set -eu

BASE_URL="${1:-http://127.0.0.1:18080}"
SERVICE="pulsix-agent"
BIN="/usr/local/bin/pulsix-agent"
DATA_DIR="/etc/pulsix"
ENV_FILE="$DATA_DIR/agent.env"
UNIT="/etc/systemd/system/$SERVICE.service"
WANTS_LINK="/etc/systemd/system/multi-user.target.wants/$SERVICE.service"
# Well-formed (enrolltoken.ParseAgent accepts it) but unknown to any
# manager: a malformed one would make the agent exit at once.
TOKEN="pxa1.ASNFZ4mrze8BI0VniavN7w.ERERERERERERERERERERERERERERERERERERERERERE.IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI"

WORK="$(mktemp -d)"
TOKEN_FILE="$WORK/token"

cleanup() {
	echo "install-smoke: removing the agent"
	sudo systemctl disable --now "$SERVICE" 2> /dev/null || true
	sudo rm -f "$UNIT" "$WANTS_LINK" "$BIN"
	sudo systemctl daemon-reload || true
	sudo rm -rf "$DATA_DIR" "$WORK"
}
trap cleanup EXIT

diagnostics() {
	echo "--- systemctl status $SERVICE"
	sudo systemctl status --no-pager "$SERVICE" || true
	echo "--- journalctl -u $SERVICE"
	sudo journalctl --no-pager -u "$SERVICE" || true
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
	sudo stat -c '%U %a' "$1"
}

# starts_logged: how many times the agent has logged its start. The
# journal keeps the lines of earlier runs, so the count only ever grows,
# also across an uninstall.
starts_logged() {
	sudo journalctl --no-pager -o cat -u "$SERVICE" 2> /dev/null | grep -c 'pulsix-agent starting' || true
}

service_active() {
	sudo systemctl is-active --quiet "$SERVICE"
}

# assert_installed N: the service runs and the journal holds exactly N
# starts, and every file the installer wrote carries its protection. Any
# start beyond N means the agent exited and systemd started it again,
# which an active service alone would hide.
assert_installed() {
	want_starts="$1"
	tries=0
	while :; do
		running=no
		if service_active; then
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
	# Longer than the unit's RestartSec (5 s), so an agent that exits and
	# is restarted shows up as a further start within it.
	sleep 6
	service_active || fail "the service did not stay active"
	after="$(starts_logged)"
	[ "$after" = "$starts" ] || fail "the agent restarted: the start count went from $starts to $after"

	[ -x "$BIN" ] || fail "$BIN is missing or not executable"
	[ -f "$UNIT" ] || fail "$UNIT is missing"
	sudo systemctl is-enabled --quiet "$SERVICE" || fail "$SERVICE is not enabled"
	got="$(owner_mode "$DATA_DIR")"
	[ "$got" = "root 700" ] || fail "$DATA_DIR is '$got', want 'root 700'"
	got="$(owner_mode "$ENV_FILE")"
	[ "$got" = "root 600" ] || fail "$ENV_FILE is '$got', want 'root 600'"
	got="$(owner_mode "$UNIT")"
	[ "$got" = "root 644" ] || fail "$UNIT is '$got', want 'root 644'"
	sudo grep -qx 'PULSIX_MANAGER_ADDR=127.0.0.1:1' "$ENV_FILE" || fail "$ENV_FILE does not name the manager"
	echo "install-smoke: install OK (active, protected, $starts start(s) logged)"
}

# assert_service_removed: the service is stopped, disabled and its unit
# and binary are gone. The data directory is the caller's to check.
assert_service_removed() {
	if service_active; then
		fail "$SERVICE is still active after the uninstall"
	fi
	[ ! -e "$UNIT" ] || fail "$UNIT is still there after the uninstall"
	if [ -e "$WANTS_LINK" ] || [ -L "$WANTS_LINK" ]; then
		fail "$WANTS_LINK is still there: the unit was not disabled"
	fi
	[ ! -e "$BIN" ] || fail "$BIN is still there after the uninstall"
}

# assert_netlink_allowed: the unit's RestrictAddressFamilies, as systemd
# loaded it, lets a process read the network interfaces. Go reads them
# over a netlink route socket, as `ip` does, so `ip -br addr` in a
# transient unit with the same filter stands in for the agent's facts and
# its network check. The same command with AF_NETLINK left out must fail,
# or the check would not tell the two apart.
assert_netlink_allowed() {
	families="$(systemctl show -p RestrictAddressFamilies --value "$SERVICE")"
	case " $families " in
	*" AF_NETLINK "*) ;;
	*) fail "the unit's RestrictAddressFamilies is '$families', which leaves out AF_NETLINK" ;;
	esac
	sudo systemd-run --quiet --wait --pipe --collect -p "RestrictAddressFamilies=$families" ip -br addr ||
		fail "'ip -br addr' failed under RestrictAddressFamilies=$families"
	if sudo systemd-run --quiet --wait --pipe --collect -p "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6" ip -br addr > /dev/null 2>&1; then
		fail "'ip -br addr' also ran without AF_NETLINK, so the netlink check proves nothing"
	fi
	echo "install-smoke: netlink OK (RestrictAddressFamilies=$families)"
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

# uninstall_agent [--keep-data]: the documented `sh -s -- --uninstall`
# invocation. Its output is echoed and kept in UNINSTALL_OUT.
uninstall_agent() {
	fetch_installer
	# shellcheck disable=SC2024
	UNINSTALL_OUT="$(sudo sh -s -- --uninstall "$@" < "$WORK/install.sh")" ||
		fail "install.sh --uninstall $* failed: $UNINSTALL_OUT"
	printf '%s\n' "$UNINSTALL_OUT"
}

# assert_output TEXT: the last uninstall printed the line TEXT.
assert_output() {
	printf '%s\n' "$UNINSTALL_OUT" | grep -qxF "$1" || fail "the uninstall did not print '$1'"
}

HINT="pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page"
NOTHING="pulsix-agent: nothing to remove"

# A fresh runner has no starts in its journal; counting from what is
# there keeps the arithmetic right if it ever does.
base_starts="$(starts_logged)"
[ -n "$base_starts" ] || base_starts=0

echo "install-smoke: fresh install"
install_agent
assert_installed $((base_starts + 1))
assert_netlink_allowed

# The re-run must restart the running agent on the new binary (backlog
# 3.49): enable --now alone left the old process running.
echo "install-smoke: upgrade over the running agent"
previous_starts="$(starts_logged)"
install_agent
assert_installed $((previous_starts + 1))

echo "install-smoke: uninstall with --keep-data"
uninstall_agent --keep-data
assert_output "$HINT"
assert_service_removed
got="$(owner_mode "$DATA_DIR")"
[ "$got" = "root 700" ] || fail "--keep-data left $DATA_DIR as '$got', want 'root 700'"
sudo test -f "$ENV_FILE" || fail "--keep-data did not keep $ENV_FILE"

echo "install-smoke: reinstall over the kept data directory"
previous_starts="$(starts_logged)"
install_agent
assert_installed $((previous_starts + 1))

echo "install-smoke: uninstall"
uninstall_agent
assert_output "$HINT"
assert_service_removed
sudo test ! -e "$DATA_DIR" || fail "$DATA_DIR is still there after the uninstall"
load_state="$(systemctl show -p LoadState --value "$SERVICE")"
[ "$load_state" = "not-found" ] || fail "systemd still knows $SERVICE (LoadState=$load_state), want not-found"

echo "install-smoke: uninstall again, with nothing installed"
uninstall_agent
assert_output "$NOTHING"

echo "install-smoke: PASS"
