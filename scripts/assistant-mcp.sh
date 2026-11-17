#!/usr/bin/env bash
# assistant-mcp.sh — connect Claude Code to one or more Pulsix instances
# through their assistant endpoint (/api/mcp), one MCP server per instance.
#
#   assistant-mcp.sh add <name> <pulsix-url>   store a token, write the helper, register the server
#   assistant-mcp.sh token <name>              replace the stored token (after a rotation)
#   assistant-mcp.sh check <name>              ask the endpoint whether it accepts the token
#   assistant-mcp.sh list                      the instances set up here
#   assistant-mcp.sh remove <name>             unregister, delete the token and the helper
#
# Per instance <name> (lowercase letters, digits and "-"):
#   - Claude Code server   pulsix-<name>   (claude mcp, user scope)
#   - token                macOS: keychain item pulsix-assistant-<name>, account
#                          $USER; elsewhere: ~/.config/pulsix/<name>.token (mode 600)
#   - headers helper       ~/.config/pulsix/<name>-headers.sh (mode 700), which
#                          Claude Code runs as headersHelper on each connection
#   - endpoint             <pulsix-url>/api/mcp, kept in ~/.config/pulsix/<name>.url
#
# The token never goes into an argument, the Claude Code configuration or the
# shell history: "add" and "token" prompt for it, and the helper reads it each
# time Claude Code connects. Create the token in Pulsix first (Settings ->
# Organization -> API tokens, kind "Assistant token") and turn on Settings ->
# Organization -> General -> Assistant access. See the user guide "Assistant
# access" in the Pulsix docs.
#
# PULSIX_MCP_DIR overrides ~/.config/pulsix. PULSIX_MCP_STORE=file uses the
# token file on macOS too.
set -euo pipefail

me="$(basename "$0")"
dir="${PULSIX_MCP_DIR:-$HOME/.config/pulsix}"

die() {
  echo "$me: $*" >&2
  exit 1
}

usage() {
  sed -n '5,9s/^#   assistant-mcp\.sh /  '"$me"' /p' "$0" >&2
  exit 2
}

check_name() {
  case "$1" in
    "" | -* | *[!a-z0-9-]*) die "instance name \"$1\" must be lowercase letters, digits and \"-\"" ;;
  esac
}

server() { printf 'pulsix-%s' "$1"; }
service() { printf 'pulsix-assistant-%s' "$1"; }
helper() { printf '%s/%s-headers.sh' "$dir" "$1"; }
tokenfile() { printf '%s/%s.token' "$dir" "$1"; }

# The Claude Code CLI: $CLAUDE_BIN, else claude on PATH, else its default
# install locations (the native installer puts it in ~/.local/bin, which a
# login shell does not always have on PATH).
find_claude() {
  local c
  for c in "${CLAUDE_BIN:-}" "$(command -v claude 2>/dev/null || true)" \
    "$HOME/.local/bin/claude" "$HOME/.claude/local/claude"; do
    if [ -n "$c" ] && [ -x "$c" ]; then
      printf '%s' "$c"
      return 0
    fi
  done
  return 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

# keychain or file, decided once per run.
store="${PULSIX_MCP_STORE:-}"
if [ -z "$store" ]; then
  if command -v security >/dev/null 2>&1; then store="keychain"; else store="file"; fi
fi
case "$store" in
  keychain | file) ;;
  *) die "PULSIX_MCP_STORE must be keychain or file" ;;
esac

store_token() {
  local name="$1" token
  if [ "$store" = keychain ]; then
    echo "Paste the assistant token for $name (hidden; the keychain asks twice)."
    # -U updates an existing item; -w as the last option makes security prompt.
    security add-generic-password -U -a "$USER" -s "$(service "$name")" \
      -l "Pulsix assistant token ($name)" -w ||
      die "storing the token in the keychain failed"
  else
    printf 'Paste the assistant token for %s (hidden): ' "$name"
    IFS= read -r -s token
    echo
    [ -n "$token" ] || die "no token given"
    (umask 077 && printf '%s\n' "$token" >"$(tokenfile "$name")")
  fi
}

endpoint_of() {
  local name="$1" file="$dir/$1.url"
  [ -f "$file" ] || die "instance $name is not set up here (no $file); run: $me add $name <pulsix-url>"
  cat "$file"
}

write_helper() {
  local name="$1" endpoint="$2" read_cmd
  if [ "$store" = keychain ]; then
    read_cmd="security find-generic-password -a \"\$USER\" -s \"$(service "$name")\" -w"
  else
    read_cmd="cat \"$(tokenfile "$name")\""
  fi
  # The helper names where the token is; it holds no secret itself.
  (umask 077 && cat >"$(helper "$name")" <<EOF
#!/bin/sh
# Written by $me for the Pulsix instance "$name" ($endpoint).
# Claude Code runs it as headersHelper each time it connects.
token=\$($read_cmd) || {
  echo "no stored token for $name; run: $me token $name" >&2
  exit 1
}
printf '{"Authorization": "Bearer %s"}' "\$token"
EOF
  )
  chmod 700 "$(helper "$name")"
}

cmd_add() {
  [ "$#" -eq 2 ] || usage
  local name="$1" url="$2" endpoint json claude
  check_name "$name"
  case "$url" in
    https://*) ;;
    *) die "the Pulsix URL must start with https://" ;;
  esac
  url="${url%/}"
  url="${url%/api/mcp}"
  case "$url" in
    https://*/*) die "give the address you open Pulsix at, without a path (got $url)" ;;
  esac
  endpoint="$url/api/mcp"
  claude="$(find_claude)" || die "the Claude Code CLI (claude) was not found; set CLAUDE_BIN to its path"
  [ "$store" = file ] || need security

  if "$claude" mcp get "$(server "$name")" >/dev/null 2>&1; then
    die "Claude Code already has a server $(server "$name"); run: $me remove $name"
  fi

  mkdir -p "$dir"
  chmod 700 "$dir"
  store_token "$name"
  write_helper "$name" "$endpoint"
  printf '%s\n' "$endpoint" >"$dir/$name.url"

  json="$(printf '{"type": "http", "url": "%s", "headersHelper": "%s"}' "$endpoint" "$(helper "$name")")"
  "$claude" mcp add-json --scope user "$(server "$name")" "$json" >/dev/null ||
    die "claude mcp add-json failed; the token and helper are in place, run: $me remove $name and try again"

  echo "Added $(server "$name") -> $endpoint"
  echo "Next: $me check $name, then start a new Claude Code session and run /mcp."
}

cmd_token() {
  [ "$#" -eq 1 ] || usage
  check_name "$1"
  [ "$store" = file ] || need security
  endpoint_of "$1" >/dev/null
  store_token "$1"
  echo "Token for $1 replaced. Claude Code picks it up on its next connection."
}

cmd_check() {
  [ "$#" -eq 1 ] || usage
  local name="$1" endpoint code
  check_name "$name"
  need curl
  endpoint="$(endpoint_of "$name")"
  [ -x "$(helper "$name")" ] || die "the helper $(helper "$name") is missing; run: $me remove $name, then add it again"
  # The header goes to curl on stdin, never into its arguments.
  code="$("$(helper "$name")" | sed -E 's/^\{"Authorization": "(.*)"\}$/Authorization: \1/' |
    curl -sS -o /dev/null -w '%{http_code}' --max-time 15 -X POST "$endpoint" \
      -H @- -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
      -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"pulsix-assistant-mcp","version":"1"}}}')" ||
    die "cannot reach $endpoint"
  case "$code" in
    200) echo "$(server "$name"): $endpoint accepts the token" ;;
    401) die "$endpoint refused the token (401): revoked, expired, or not an assistant token" ;;
    403) die "$endpoint refused the token (403): is assistant access turned on for the organization?" ;;
    *) die "$endpoint answered HTTP $code" ;;
  esac
}

cmd_list() {
  [ "$#" -eq 0 ] || usage
  local f name found=0
  for f in "$dir"/*.url; do
    [ -f "$f" ] || continue
    found=1
    name="$(basename "$f" .url)"
    printf '%-24s %s\n' "$(server "$name")" "$(cat "$f")"
  done
  [ "$found" = 1 ] || echo "no Pulsix instances set up here; run: $me add <name> <pulsix-url>"
}

cmd_remove() {
  [ "$#" -eq 1 ] || usage
  local name="$1" claude
  check_name "$name"
  if claude="$(find_claude)"; then
    "$claude" mcp remove --scope user "$(server "$name")" >/dev/null 2>&1 || true
  fi
  if command -v security >/dev/null 2>&1; then
    security delete-generic-password -a "$USER" -s "$(service "$name")" >/dev/null 2>&1 || true
  fi
  rm -f "$(helper "$name")" "$(tokenfile "$name")" "$dir/$name.url"
  echo "Removed $(server "$name") (server, stored token and helper). Revoke the token in Pulsix too."
}

[ "$#" -ge 1 ] || usage
cmd="$1"
shift
case "$cmd" in
  add) cmd_add "$@" ;;
  token) cmd_token "$@" ;;
  check) cmd_check "$@" ;;
  list) cmd_list "$@" ;;
  remove) cmd_remove "$@" ;;
  *) usage ;;
esac
