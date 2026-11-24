#!/bin/sh
# The Pulsix agent installer: downloads the agent binary from the manager
# it is being installed against, writes the environment file carrying the
# enrollment token, installs the service definition and starts the service:
# a systemd unit on Linux, a launchd daemon on macOS (Windows has its own
# installer, install.ps1).
#
# The manager serves this script templated (GET /install.sh): BASE_URL is
# the manager's own public URL, the unit heredoc carries the systemd unit
# (deploy/agent/systemd/pulsix-agent.service) and the plist heredoc the
# launchd plist (deploy/agent/launchd/dev.noix.pulsix-agent.plist), both
# embedded into the script. Drift between the copies is guarded by a test
# (internal/agentdist).
#
# Usage:
#   curl -fsSL https://<manager>/install.sh | sudo sh -s -- \
#     --manager <manager>:8444 \
#     [--name <agent name>] [--server-name <manager certificate name>] \
#     [--token-file <path> | --token pxa1.<id>.<secret>.<pin>]
#
# The token is optional on a re-run (see below); a first install needs it.
#
#   curl -fsSL https://<manager>/install.sh | sudo sh -s -- --uninstall [--keep-data]
#
# The token and manager address are the two values the Agents page prints.
# Without --token-file or --token the script asks for the token on the
# terminal, without echoing it. That is the default on purpose (backlog
# 7.30): an argument is in the process list, where every local user of the
# host can read it for as long as the install runs. --token-file names a
# file only root can read, for unattended installs; --token still works
# and still puts the token in the process list.
# The token is optional on a re-run (backlog 3.60): when the data directory
# already holds the agent's identity (agent.crt, agent.key and agent.ca.pem,
# each a non-empty file) and neither --token nor --token-file is given, the
# script asks for nothing and writes agent.env without the token. An
# upgrade, or a reinstall after --uninstall --keep-data, needs none.
# --server-name is the manager gRPC certificate's DNS SAN (the manager's
# own PULSIX_GRPC_SERVER_NAME) for deployments where it is not the host
# being dialed — the same override the worker enrollment snippet passes.
# The token is written owner-only into /etc/pulsix/agent.env (on macOS
# "/Library/Application Support/Pulsix/agent.env", owned by root: the agent
# refuses an env file anyone else can read or write) and the agent
# forgets it as soon as a valid identity exists (first start enrolls,
# restarts reconnect).
#
# A re-run upgrades: it replaces the binary and restarts the service. It
# needs no token while the identity is there (see above).
#
# --uninstall removes the agent from the host (backlog 3.49): it stops the
# service and removes the service definition, the binary, the macOS log
# directory and the data directory (identity, certificate, agent.env,
# agent.yaml). --keep-data keeps the data directory, so a later install
# reuses the identity. It needs no manager and no token, takes no install
# flag, and removing something not installed succeeds quietly. The agent
# stays registered in Pulsix: revoke or delete it on the Agents page.

set -eu

BASE_URL="{{.BaseURL}}"
BIN_DIR="/usr/local/bin"
SERVICE_NAME="pulsix-agent"

PULSIX_TOKEN=""
PULSIX_TOKEN_FILE=""
PULSIX_MANAGER=""
PULSIX_NAME=""
PULSIX_SERVER_NAME=""
PULSIX_UNINSTALL=""
PULSIX_KEEP_DATA=""
# Set by any install flag, even one given an empty value: --uninstall takes none.
PULSIX_INSTALL_FLAGS=""

die() {
	echo "install.sh: $1" >&2
	exit 1
}

usage() {
	echo "usage: install.sh --manager <manager>:8444 [--name <agent name>] [--server-name <manager certificate name>] [--token-file <path> | --token pxa1.<id>.<secret>.<pin>]" >&2
	echo "       (the token is optional on a re-run over an existing identity)" >&2
	echo "       install.sh --uninstall [--keep-data]" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--token)
		[ $# -ge 2 ] || usage
		PULSIX_TOKEN="$2"
		PULSIX_INSTALL_FLAGS=1
		shift 2
		;;
	--token-file)
		[ $# -ge 2 ] || usage
		PULSIX_TOKEN_FILE="$2"
		PULSIX_INSTALL_FLAGS=1
		shift 2
		;;
	--manager)
		[ $# -ge 2 ] || usage
		PULSIX_MANAGER="$2"
		PULSIX_INSTALL_FLAGS=1
		shift 2
		;;
	--name)
		[ $# -ge 2 ] || usage
		PULSIX_NAME="$2"
		PULSIX_INSTALL_FLAGS=1
		shift 2
		;;
	--server-name)
		[ $# -ge 2 ] || usage
		PULSIX_SERVER_NAME="$2"
		PULSIX_INSTALL_FLAGS=1
		shift 2
		;;
	--uninstall)
		PULSIX_UNINSTALL=1
		shift
		;;
	--keep-data)
		PULSIX_KEEP_DATA=1
		shift
		;;
	*)
		usage
		;;
	esac
done

if [ -n "$PULSIX_UNINSTALL" ]; then
	[ -z "$PULSIX_INSTALL_FLAGS" ] || usage
else
	[ -z "$PULSIX_KEEP_DATA" ] || usage
	[ -n "$PULSIX_MANAGER" ] || usage
fi

# Per-system values. Linux keeps its systemd layout, macOS gets a launchd
# daemon; anything else has no shell installer.
SYSTEM="$(uname -s)"
case "$SYSTEM" in
Linux)
	PULSIX_OS="linux"
	DATA_DIR="/etc/pulsix"
	ENV_FILE="$DATA_DIR/agent.env"
	UNIT_PATH="/etc/systemd/system/pulsix-agent.service"
	;;
Darwin)
	PULSIX_OS="darwin"
	DATA_DIR="/Library/Application Support/Pulsix"
	ENV_FILE="$DATA_DIR/agent.env"
	LOG_DIR="/Library/Logs/Pulsix"
	LAUNCHD_LABEL="dev.noix.pulsix-agent"
	PLIST_PATH="/Library/LaunchDaemons/$LAUNCHD_LABEL.plist"
	;;
*) die "unsupported system $SYSTEM (Linux, macOS and Windows have agents; on Windows use install.ps1)" ;;
esac

if [ "$(id -u)" != "0" ]; then
	[ -z "$PULSIX_UNINSTALL" ] || die "must run as root (e.g. curl ... | sudo sh -s -- --uninstall)"
	die "must run as root (e.g. curl ... | sudo sh -s -- --manager ...)"
fi

# wait_for_launchd_job_gone: bootout can return before the job is gone (the
# agent is still handling SIGTERM), and bootstrapping a label that is still
# loaded fails, so the upgrade and the uninstall wait for it to disappear.
# An argument, if given, says what the timeout left untouched.
wait_for_launchd_job_gone() {
	tries=0
	while launchctl print "system/$LAUNCHD_LABEL" > /dev/null 2>&1; do
		tries=$((tries + 1))
		if [ "$tries" -gt 20 ]; then
			if [ -n "${1:-}" ]; then
				die "the running $LAUNCHD_LABEL did not stop within 20 seconds; $1. Run 'launchctl bootout system/$LAUNCHD_LABEL' and retry"
			fi
			die "the running $LAUNCHD_LABEL did not stop within 20 seconds; run 'launchctl bootout system/$LAUNCHD_LABEL' and retry"
		fi
		sleep 1
	done
}

# remove_path PATH: removes PATH when it exists and says so. Only the
# script's fixed absolute paths are passed; anything else (an empty or
# relative path) is refused rather than handed to rm -rf. PATH never
# carries a trailing slash, so a symlink is removed as the link, never
# followed into its target.
REMOVED=""
remove_path() {
	case "$1" in
	/?*) ;;
	*) die "refusing to remove '$1'" ;;
	esac
	if [ -e "$1" ] || [ -L "$1" ]; then
		rm -rf -- "$1"
		echo "pulsix-agent: removed $1"
		REMOVED=1
	fi
}

# remove_data_dir: the data directory, unless --keep-data keeps it as it is.
remove_data_dir() {
	if [ -z "$PULSIX_KEEP_DATA" ]; then
		remove_path "$DATA_DIR"
	elif [ -e "$DATA_DIR" ]; then
		echo "pulsix-agent: kept $DATA_DIR (--keep-data)"
	fi
}

# uninstall_linux: stop and disable the unit where systemd runs (a unit
# that is not loaded is not an error), then remove the unit, the binary
# and the data directory. A unit still active afterwards stops the
# uninstall before anything is removed, so a running agent never loses its
# binary. Logs are in the journal; nothing to remove.
uninstall_linux() {
	if [ -d /run/systemd/system ] && [ -f "$UNIT_PATH" ]; then
		systemctl disable --now "$SERVICE_NAME" 2> /dev/null || true
		if systemctl is-active --quiet "$SERVICE_NAME"; then
			die "could not stop $SERVICE_NAME; nothing was removed. Run 'systemctl stop $SERVICE_NAME' and retry"
		fi
		echo "pulsix-agent: stopped and disabled $SERVICE_NAME"
	fi
	remove_path "$UNIT_PATH"
	if [ -d /run/systemd/system ]; then
		systemctl daemon-reload || true
	fi
	remove_path "$BIN_DIR/pulsix-agent"
	remove_data_dir
}

# uninstall_darwin: unload the daemon and wait for it to go, then remove
# the plist, the binary, the logs and the data directory.
uninstall_darwin() {
	if launchctl print "system/$LAUNCHD_LABEL" > /dev/null 2>&1; then
		launchctl bootout "system/$LAUNCHD_LABEL" 2> /dev/null || true
		wait_for_launchd_job_gone "nothing was removed"
		echo "pulsix-agent: stopped $LAUNCHD_LABEL"
		REMOVED=1
	fi
	remove_path "$PLIST_PATH"
	remove_path "$BIN_DIR/pulsix-agent"
	remove_path "$LOG_DIR"
	remove_data_dir
}

if [ -n "$PULSIX_UNINSTALL" ]; then
	case "$SYSTEM" in
	Linux) uninstall_linux ;;
	Darwin) uninstall_darwin ;;
	esac
	if [ -z "$REMOVED" ]; then
		echo "pulsix-agent: nothing to remove"
		exit 0
	fi
	echo "pulsix-agent: the agent is still registered in Pulsix; revoke or delete it on the Agents page"
	exit 0
fi

# read_token_file PATH: the token from a regular file root owns that no
# group or other user may read or write, so the secret never passes
# through anyone else's hands on its way here.
read_token_file() {
	[ -f "$1" ] && [ ! -L "$1" ] || die "token file $1 is not a regular file"
	case "$SYSTEM" in
	Darwin) owner_mode="$(stat -f '%u %Lp' "$1" 2> /dev/null)" || die "cannot inspect token file $1" ;;
	*) owner_mode="$(stat -c '%u %a' "$1" 2> /dev/null)" || die "cannot inspect token file $1" ;;
	esac
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

# has_identity DIR: succeeds when DIR holds the agent's identity, which is
# exactly what the agent needs to start without enrolling: the certificate,
# its key and the CA chain it dials with (agent.crt, agent.key,
# agent.ca.pem; internal/agent/config.go), each a non-empty regular file.
# agent.json only carries bookkeeping and is not required. Anything less is
# treated as no identity, so the token is asked for as before.
has_identity() {
	for identity_file in agent.crt agent.key agent.ca.pem; do
		[ -f "$1/$identity_file" ] && [ ! -L "$1/$identity_file" ] && [ -s "$1/$identity_file" ] || return 1
	done
}

# An identity already on disk makes the token unnecessary (backlog 3.60):
# the agent enrolls only while it holds no valid identity and forgets the
# token afterwards anyway (internal/agent/loop.go). A token given on the
# command line is still read, validated and written as always.
PULSIX_REUSE_IDENTITY=""
if [ -n "$PULSIX_TOKEN_FILE" ]; then
	[ -z "$PULSIX_TOKEN" ] || die "--token and --token-file exclude each other"
	PULSIX_TOKEN="$(read_token_file "$PULSIX_TOKEN_FILE")"
elif [ -z "$PULSIX_TOKEN" ]; then
	if has_identity "$DATA_DIR"; then
		PULSIX_REUSE_IDENTITY=1
		echo "pulsix-agent: reusing the existing identity in $DATA_DIR; no enrollment token needed"
	else
		PULSIX_TOKEN="$(read_token_tty)"
	fi
fi
if [ -z "$PULSIX_REUSE_IDENTITY" ]; then
	[ -n "$PULSIX_TOKEN" ] || die "no enrollment token given"
fi
if [ "$SYSTEM" = "Linux" ]; then
	[ -d /run/systemd/system ] || die "systemd is required (no /run/systemd/system found)"
fi

ARCH="$(uname -m)"
case "$SYSTEM:$ARCH" in
Linux:x86_64 | Darwin:x86_64) PULSIX_ARCH="amd64" ;;
Linux:aarch64 | Linux:arm64 | Darwin:arm64) PULSIX_ARCH="arm64" ;;
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

# sha256_of FILE: the hex digest. macOS has no sha256sum, only shasum.
sha256_of() {
	if command -v sha256sum > /dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f1
	elif command -v shasum > /dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f1
	else
		die "neither sha256sum nor shasum is available"
	fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "pulsix-agent: downloading the binary for $PULSIX_OS/$PULSIX_ARCH from $BASE_URL"
fetch "$BASE_URL/agents/bin/$PULSIX_OS/$PULSIX_ARCH" "$TMP/pulsix-agent" || die "download failed"
fetch "$BASE_URL/agents/bin/$PULSIX_OS/$PULSIX_ARCH.sha256" "$TMP/pulsix-agent.sha256" || die "checksum download failed"

EXPECTED="$(cut -d ' ' -f1 "$TMP/pulsix-agent.sha256" | head -n1)"
case "$EXPECTED" in
[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
*) die "the manager returned an unusable checksum" ;;
esac
ACTUAL="$(sha256_of "$TMP/pulsix-agent")"
[ "$ACTUAL" = "$EXPECTED" ] || die "checksum mismatch (got $ACTUAL, expected $EXPECTED — do not install an unverified binary)"

# A new Apple silicon Mac has no /usr/local/bin.
mkdir -p "$BIN_DIR"
install -m 0755 "$TMP/pulsix-agent" "$BIN_DIR/pulsix-agent"

mkdir -p "$DATA_DIR"
chmod 700 "$DATA_DIR"
umask 077
{
	echo "PULSIX_MANAGER_ADDR=$PULSIX_MANAGER"
	if [ -z "$PULSIX_REUSE_IDENTITY" ]; then
		echo "PULSIX_ENROLL_TOKEN=$PULSIX_TOKEN"
	fi
	if [ -n "$PULSIX_NAME" ]; then
		echo "PULSIX_AGENT_NAME=$PULSIX_NAME"
	fi
	if [ -n "$PULSIX_SERVER_NAME" ]; then
		echo "PULSIX_MANAGER_SERVER_NAME=$PULSIX_SERVER_NAME"
	fi
} > "$ENV_FILE"
chmod 600 "$ENV_FILE"

case "$SYSTEM" in
Linux)
	# The systemd unit is written verbatim (the quoted heredoc delimiter
	# keeps the shell from expanding anything inside it).
	cat > "$UNIT_PATH" <<'PULSIX_UNIT_EOF'
{{.Unit}}
PULSIX_UNIT_EOF

	chmod 644 "$UNIT_PATH"

	# A re-run upgrades: enable --now would leave a running unit on the
	# old binary, so the unit is restarted (which starts a stopped one).
	systemctl daemon-reload
	systemctl enable "$SERVICE_NAME"
	systemctl restart "$SERVICE_NAME"

	echo "pulsix-agent: installed and started ($SERVICE_NAME)"
	echo "pulsix-agent: identity lives in /etc/pulsix — the enrollment token is spent on first start"
	;;
Darwin)
	# launchd has no EnvironmentFile: the agent reads the env file itself
	# and refuses one that is not root-owned and private.
	chown root:wheel "$DATA_DIR" "$ENV_FILE"

	# The agent creates its log file (private) when it opens it; the
	# directory is made here so it exists with the right owner.
	mkdir -p "$LOG_DIR"
	chown root:wheel "$LOG_DIR"
	chmod 700 "$LOG_DIR"

	# The plist is written verbatim, like the systemd unit.
	cat > "$PLIST_PATH" <<'PULSIX_PLIST_EOF'
{{.Plist}}
PULSIX_PLIST_EOF

	chown root:wheel "$PLIST_PATH"
	chmod 644 "$PLIST_PATH"

	# A re-run upgrades: unload the running daemon (if any), wait for it
	# to go, then load.
	launchctl bootout "system/$LAUNCHD_LABEL" 2> /dev/null || true
	wait_for_launchd_job_gone
	launchctl bootstrap system "$PLIST_PATH" || die "launchctl bootstrap failed; check 'launchctl print system/$LAUNCHD_LABEL' and $LOG_DIR/agent.log"

	echo "pulsix-agent: installed and started ($LAUNCHD_LABEL)"
	echo "pulsix-agent: identity lives in $DATA_DIR — the enrollment token is spent on first start"
	;;
esac
