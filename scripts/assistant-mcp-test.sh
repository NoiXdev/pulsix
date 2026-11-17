#!/usr/bin/env bash
# assistant-mcp-test.sh — proves scripts/assistant-mcp.sh registers the
# assistant endpoint the way the manager serves it: an organization token at
# <url>/api/mcp, a personal token for one organization at
# <url>/api/mcp/o/<slug> (--org), one registered server per name, so a
# personal token used in two organizations is added twice. It also checks
# that only Pulsix tokens (pxt_, pxp_) are accepted, that a bad slug is
# refused, that "check" explains a 400 with the endpoint's detail, and that
# the token never shows up in the output or in a command's arguments.
#
# It runs against a fake Claude Code CLI ($CLAUDE_BIN) and a fake curl on
# PATH, with the token stored in a file under a temporary $PULSIX_MCP_DIR,
# so it needs neither the network nor a keychain. The token comes on stdin.
#
# Usage: scripts/assistant-mcp-test.sh

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/assistant-mcp.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir "$work/bin"
cat >"$work/bin/claude" <<'FAKE'
#!/bin/sh
# Records every call in $FAKE_LOG; "mcp get" finds a server only once
# "mcp add-json" registered it.
set -eu
echo "claude $*" >>"$FAKE_LOG"
[ "$1" = mcp ] || exit 2
case "$2" in
get) [ -e "$FAKE_SERVERS/$3" ] ;;
add-json)
	[ "$3" = --scope ] && [ "$4" = user ] || exit 2
	printf '%s\n' "$6" >"$FAKE_SERVERS/$5"
	;;
remove) rm -f "$FAKE_SERVERS/$5" ;;
*) exit 2 ;;
esac
FAKE
cat >"$work/bin/curl" <<'FAKE'
#!/bin/sh
# Records its arguments and the header lines it reads on stdin, then answers
# $FAKE_CURL_BODY with $FAKE_CURL_CODE in the shape the script asks for
# (-o <file> -w '%{http_code}').
set -eu
echo "curl $*" >>"$FAKE_LOG"
cat >"$FAKE_CURL_STDIN"
out=/dev/null
while [ "$#" -gt 0 ]; do
	case "$1" in
	-o) out="$2"; shift ;;
	esac
	shift
done
printf '%s' "${FAKE_CURL_BODY:-}" >"$out"
printf '%s' "${FAKE_CURL_CODE:-200}"
FAKE
chmod +x "$work/bin/claude" "$work/bin/curl"

export PATH="$work/bin:$PATH"
export CLAUDE_BIN="$work/bin/claude"
export PULSIX_MCP_STORE=file
export FAKE_LOG="$work/calls.log"
export FAKE_SERVERS="$work/servers"
export FAKE_CURL_STDIN="$work/curl.stdin"

fail=0
case_name=
bad() {
	echo "assistant-mcp-test: $case_name: $*" >&2
	fail=1
}

# Each case starts from an empty configuration directory, no registered
# servers and an empty call log.
setup() {
	case_name="$1"
	rm -rf "$work/cfg" "$FAKE_SERVERS" "$FAKE_LOG" "$work/out"
	mkdir -p "$FAKE_SERVERS"
	export PULSIX_MCP_DIR="$work/cfg"
	: >"$FAKE_LOG"
}

# run <token> <args...>: the script with the token on stdin; output in $work/out.
run() {
	local token="$1"
	shift
	printf '%s\n' "$token" | "$script" "$@" >"$work/out" 2>&1
}

expect_endpoint() {
	local name="$1" want="$2" got
	got="$(cat "$PULSIX_MCP_DIR/$name.url" 2>/dev/null || true)"
	[ "$got" = "$want" ] || bad "url file of $name = \"$got\", want \"$want\""
	grep -q "\"url\": \"$want\"" "$FAKE_SERVERS/pulsix-$name" 2>/dev/null ||
		bad "mcp add-json for $name did not get $want ($(cat "$FAKE_SERVERS/pulsix-$name" 2>/dev/null || echo none))"
}

expect_secret_kept() {
	local secret="$1"
	if grep -q "$secret" "$FAKE_LOG" "$work/out"; then
		bad "the token shows up in the output or in a command's arguments"
	fi
}

# A personal token for one organization: the endpoint names it.
setup "personal token with --org"
if run pxp_personal1 add prod-acme https://pulsix.example --org acme; then
	expect_endpoint prod-acme https://pulsix.example/api/mcp/o/acme
	[ "$(cat "$PULSIX_MCP_DIR/prod-acme.token")" = pxp_personal1 ] || bad "the stored token is not the one given"
	expect_secret_kept pxp_personal1
else
	bad "add failed: $(cat "$work/out")"
fi

# The same personal token in a second organization: a second name.
case_name="the same personal token for a second organization"
if run pxp_personal1 add prod-beta https://pulsix.example/ --org beta; then
	expect_endpoint prod-beta https://pulsix.example/api/mcp/o/beta
	expect_endpoint prod-acme https://pulsix.example/api/mcp/o/acme
else
	bad "add failed: $(cat "$work/out")"
fi

# An organization token: the plain endpoint, as before.
setup "organization token without --org"
if run pxt_orgtoken1 add prod https://pulsix.example; then
	expect_endpoint prod https://pulsix.example/api/mcp
	expect_secret_kept pxt_orgtoken1
else
	bad "add failed: $(cat "$work/out")"
fi

# A URL copied with the endpoint's path is normalised.
setup "URL with /api/mcp"
run pxt_orgtoken1 add prod https://pulsix.example/api/mcp || bad "add failed: $(cat "$work/out")"
expect_endpoint prod https://pulsix.example/api/mcp

setup "URL with /api/mcp/o/<slug>"
run pxp_personal1 add prod-acme https://pulsix.example/api/mcp/o/acme || bad "add failed: $(cat "$work/out")"
expect_endpoint prod-acme https://pulsix.example/api/mcp/o/acme

setup "URL slug and --org agree"
run pxp_personal1 add prod-acme https://pulsix.example/api/mcp/o/acme/ --org acme || bad "add failed: $(cat "$work/out")"
expect_endpoint prod-acme https://pulsix.example/api/mcp/o/acme

setup "URL slug and --org differ"
if run pxp_personal1 add prod-acme https://pulsix.example/api/mcp/o/acme --org beta; then
	bad "accepted two different organizations"
fi

# Only Pulsix tokens are accepted, and the refusal does not echo the input.
setup "a token that is no Pulsix token"
if run abc add prod https://pulsix.example; then
	bad "accepted the token abc"
else
	grep -q "does not start with pxt_ or pxp_" "$work/out" || bad "unexpected refusal: $(cat "$work/out")"
	[ ! -e "$FAKE_SERVERS/pulsix-prod" ] || bad "registered a server for a refused token"
	if grep -qx "abc" "$work/out"; then bad "the refusal echoed the token"; fi
fi

setup "a slug that is not one"
if run pxp_personal1 add prod-acme https://pulsix.example --org 'Bad!'; then
	bad "accepted the slug Bad!"
else
	grep -q "organization" "$work/out" || bad "unexpected refusal: $(cat "$work/out")"
	[ ! -e "$PULSIX_MCP_DIR/prod-acme.url" ] || bad "wrote a url file for a refused slug"
fi

setup "--org without a slug"
if run pxp_personal1 add prod-acme https://pulsix.example --org; then
	bad "accepted --org without a value"
fi

setup "one server per name"
run pxp_personal1 add prod https://pulsix.example --org acme || bad "first add failed: $(cat "$work/out")"
if run pxp_personal1 add prod https://pulsix.example --org beta; then
	bad "registered a second server under the same name"
fi

# check: the header goes to curl on stdin, a 400 is explained with the
# endpoint's detail.
setup "check explains a 400"
run pxp_personal1 add prod https://pulsix.example || bad "add failed: $(cat "$work/out")"
export FAKE_CURL_CODE=400
export FAKE_CURL_BODY='{"title":"Bad Request","status":400,"detail":"this token is valid for several organizations; connect to /api/mcp/o/<organization>"}'
if "$script" check prod >"$work/out" 2>&1; then
	bad "check passed on a 400"
else
	grep -q "valid for several organizations" "$work/out" || bad "the detail is missing: $(cat "$work/out")"
	grep -q -- "--org" "$work/out" || bad "no hint to use --org: $(cat "$work/out")"
fi
grep -q "Bearer pxp_personal1" "$FAKE_CURL_STDIN" || bad "curl did not get the header on stdin"
expect_secret_kept pxp_personal1

setup "check accepts a 200"
run pxp_personal1 add prod https://pulsix.example --org acme || bad "add failed: $(cat "$work/out")"
export FAKE_CURL_CODE=200 FAKE_CURL_BODY='{}'
"$script" check prod >"$work/out" 2>&1 || bad "check failed: $(cat "$work/out")"
grep -q "https://pulsix.example/api/mcp/o/acme" "$FAKE_LOG" || bad "check did not ask the organization's endpoint"

if [ "$fail" = 0 ]; then
	echo "assistant-mcp-test: ok"
fi
exit "$fail"
