#!/bin/sh
# The Pulsix agent installer: downloads the agent binary from the manager
# it is being installed against, writes the environment file carrying the
# enrollment token, installs the systemd unit and starts the service.
#
# The manager serves this script templated (GET /install.sh): BASE_URL is
# the manager's own public URL and the unit heredoc carries the systemd
# unit (deploy/agent/systemd/pulsix-agent.service) embedded into the
# script. Drift between the two copies is guarded by a test
# (internal/agentdist).
#
# Usage:
#   curl -fsSL https://<manager>/install.sh | sudo sh -s -- \
#     --manager <manager>:8444 \
#     [--name <agent name>] [--server-name <manager certificate name>] \
#     [--token-file <path> | --token pxa1.<id>.<secret>.<pin>]
#
# The token and manager address are the two values the Agents page prints.
# Without --token-file or --token the script asks for the token on the
# terminal, without echoing it. That is the default on purpose (backlog
# 7.30): an argument is in the process list, where every local user of the
# host can read it for as long as the install runs. --token-file names a
# file only root can read, for unattended installs; --token still works
# and still puts the token in the process list.
# --server-name is the manager gRPC certificate's DNS SAN (the manager's
# own PULSIX_GRPC_SERVER_NAME) for deployments where it is not the host
# being dialed — the same override the worker enrollment snippet passes.
# The token is written owner-only into /etc/pulsix/agent.env and the agent
# forgets it as soon as a valid identity exists (first start enrolls,
# restarts reconnect).

set -eu

BASE_URL="{{.BaseURL}}"
BIN_DIR="/usr/local/bin"
ENV_FILE="/etc/pulsix/agent.env"
UNIT_PATH="/etc/systemd/system/pulsix-agent.service"
SERVICE_NAME="pulsix-agent"

PULSIX_TOKEN=""
PULSIX_TOKEN_FILE=""
PULSIX_MANAGER=""
PULSIX_NAME=""
PULSIX_SERVER_NAME=""

die() {
	echo "install.sh: $1" >&2
	exit 1
}

usage() {
	echo "usage: install.sh --manager <manager>:8444 [--name <agent name>] [--server-name <manager certificate name>] [--token-file <path> | --token pxa1.<id>.<secret>.<pin>]" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--token)
		[ $# -ge 2 ] || usage
		PULSIX_TOKEN="$2"
		shift 2
		;;
	--token-file)
		[ $# -ge 2 ] || usage
		PULSIX_TOKEN_FILE="$2"
		shift 2
		;;
	--manager)
		[ $# -ge 2 ] || usage
		PULSIX_MANAGER="$2"
		shift 2
		;;
	--name)
		[ $# -ge 2 ] || usage
		PULSIX_NAME="$2"
		shift 2
		;;
	--server-name)
		[ $# -ge 2 ] || usage
		PULSIX_SERVER_NAME="$2"
		shift 2
		;;
	*)
		usage
		;;
	esac
done

[ -n "$PULSIX_MANAGER" ] || usage
[ "$(id -u)" = "0" ] || die "must run as root (e.g. curl ... | sudo sh -s -- --manager ...)"

# read_token_file PATH: the token from a regular file root owns that no
# group or other user may read or write, so the secret never passes
# through anyone else's hands on its way here.
read_token_file() {
	[ -f "$1" ] && [ ! -L "$1" ] || die "token file $1 is not a regular file"
	owner_mode="$(stat -c '%u %a' "$1" 2> /dev/null)" || die "cannot inspect token file $1"
	case "$owner_mode" in
	"0 400" | "0 600") ;;
	*) die "token file $1 must be owned by root with mode 0600 or 0400 (found uid and mode $owner_mode)" ;;
	esac
	head -n1 "$1" | tr -d '[:space:]'
}

# read_token_tty: the token from the terminal, without echo. stdin is this
# script itself (sh -s), so the prompt reads /dev/tty.
read_token_tty() {
	(: < /dev/tty) 2> /dev/null || die "no terminal to ask for the token on; pass --token-file <path>"
	printf 'Enrollment token (input hidden): ' > /dev/tty
	stty -echo < /dev/tty 2> /dev/null || true
	IFS= read -r token < /dev/tty || token=""
	stty echo < /dev/tty 2> /dev/null || true
	printf '\n' > /dev/tty
	printf '%s' "$token" | tr -d '[:space:]'
}

if [ -n "$PULSIX_TOKEN_FILE" ]; then
	[ -z "$PULSIX_TOKEN" ] || die "--token and --token-file exclude each other"
	PULSIX_TOKEN="$(read_token_file "$PULSIX_TOKEN_FILE")"
elif [ -z "$PULSIX_TOKEN" ]; then
	PULSIX_TOKEN="$(read_token_tty)"
fi
[ -n "$PULSIX_TOKEN" ] || die "no enrollment token given"
[ -d /run/systemd/system ] || die "systemd is required (no /run/systemd/system found)"

ARCH="$(uname -m)"
case "$ARCH" in
x86_64) PULSIX_ARCH="amd64" ;;
aarch64 | arm64) PULSIX_ARCH="arm64" ;;
*) die "unsupported architecture $ARCH (only amd64 and arm64 have agent binaries)" ;;
esac

# fetch URL FILE: curl when present, else wget.
fetch() {
	if command -v curl > /dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget > /dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		die "neither curl nor wget is available"
	fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "pulsix-agent: downloading the binary for linux/$PULSIX_ARCH from $BASE_URL"
fetch "$BASE_URL/agents/bin/linux/$PULSIX_ARCH" "$TMP/pulsix-agent" || die "download failed"
fetch "$BASE_URL/agents/bin/linux/$PULSIX_ARCH.sha256" "$TMP/pulsix-agent.sha256" || die "checksum download failed"

EXPECTED="$(cut -d ' ' -f1 "$TMP/pulsix-agent.sha256" | head -n1)"
case "$EXPECTED" in
[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
*) die "the manager returned an unusable checksum" ;;
esac
ACTUAL="$(sha256sum "$TMP/pulsix-agent" | cut -d ' ' -f1)"
[ "$ACTUAL" = "$EXPECTED" ] || die "checksum mismatch (got $ACTUAL, expected $EXPECTED — do not install an unverified binary)"

install -m 0755 "$TMP/pulsix-agent" "$BIN_DIR/pulsix-agent"

mkdir -p /etc/pulsix
chmod 700 /etc/pulsix
umask 077
{
	echo "PULSIX_MANAGER_ADDR=$PULSIX_MANAGER"
	echo "PULSIX_ENROLL_TOKEN=$PULSIX_TOKEN"
	if [ -n "$PULSIX_NAME" ]; then
		echo "PULSIX_AGENT_NAME=$PULSIX_NAME"
	fi
	if [ -n "$PULSIX_SERVER_NAME" ]; then
		echo "PULSIX_MANAGER_SERVER_NAME=$PULSIX_SERVER_NAME"
	fi
} > "$ENV_FILE"
chmod 600 "$ENV_FILE"

# The systemd unit is written verbatim (the quoted heredoc delimiter keeps
# the shell from expanding anything inside it).
cat > "$UNIT_PATH" <<'PULSIX_UNIT_EOF'
{{.Unit}}
PULSIX_UNIT_EOF

chmod 644 "$UNIT_PATH"

systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME"

echo "pulsix-agent: installed and started ($SERVICE_NAME)"
echo "pulsix-agent: identity lives in /etc/pulsix — the enrollment token is spent on first start"
