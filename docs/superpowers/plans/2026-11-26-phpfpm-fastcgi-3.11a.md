# PHP-FPM over FastCGI directly (backlog 3.11a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `phpfpm` check reads a pool's status page by talking FastCGI to the pool's own listen address (TCP or unix socket), with no web server in front of it. HTTP entries keep working unchanged.

**Architecture:** A `urls` entry is now either the status page URL (as today) or a FastCGI address with the status path: `fcgi://127.0.0.1:9000/status` or `unix:/run/php/php8.3-fpm.sock:/status`. The grammar lives once in `internal/checkplugins` (new format `phpfpmPool`, parser `ParsePHPFPMFastCGI`), used by the manager's validation, the agent's local-check validation and the agent plugin; the SPA gets the TypeScript twin. The agent gets a minimal FastCGI responder client (`internal/agent/checks/plugins/internal/fastcgi`, one request per connection, everything bounded) and `webcheck` gets a round over per-page fetch functions, so phpfpm picks HTTP or FastCGI per entry and keeps its parsing, items, rates and pools_down as they are.

**Tech Stack:** Go (stdlib only, no new dependency), React/TypeScript (format twin, texts), testcontainers (`php:8.3-fpm-alpine`).

**Spec:** backlog row 3.11a (docs/BACKLOG.md); the 3.11 design docs/superpowers/specs/2026-11-13-web-server-checks-design.md for everything not changed here.

## Global Constraints

- No migration, no protocol change, no new dependency. HTTP entries behave byte for byte as before (existing phpfpm and webcheck tests pass unchanged).
- Failure texts stay fixed strings: never the address, the path, a header, STDERR or the body.
- Never forward arbitrary FastCGI params: the agent sends a fixed set whose only variable value is the validated status path.
- Every read is bounded (bytes and time); a context that ends returns the plugin contract's `ctx.Err()`.

## Rulings

- R1 FastCGI pools are entries of the existing `urls` list, recognised by prefix: `fcgi://HOST:PORT/PATH` (TCP) and `unix:/SOCKET:/PATH` (socket). — One list keeps "one entry = one pool = its item", the 16-pool bound and mixed HTTP/FastCGI pools in one check; `fcgi://` is the spelling other monitoring tools use for exactly this. A separate parameter would be silently ignored by an older agent, which would then read its default `http://127.0.0.1/status` instead. — If wrong: a later separate parameter needs a migration of stored entries.
- R2 Version skew: an older agent hands such an entry to its HTTP client, which refuses the scheme before dialing, so the pool reads down with "connection failed" — visible, never something else (pinned by a webcheck test). An older agent refuses such an entry in a local `agent.yaml` check (format). An older manager refuses such entries on save. — If wrong: an operator on a mixed fleet sees a pool down until the agent is updated; documented in upgrades.md.
- R3 The status path is mandatory in both forms, has no query (the agent always asks `json`) and is `/` followed by at most 254 of `A-Z a-z 0-9 _ ~ / -`: **no dot**. — Review fix round 1 (M1): the path goes only into SCRIPT_NAME / DOCUMENT_URI / REQUEST_URI; SCRIPT_FILENAME is always `/` (R6). FPM recognises its status page by the request URI derived from SCRIPT_NAME, so the page is still served (verified against real PHP-FPM 8.3), and any other path names no script: real FPM answers the same 404 for a missing file, an existing file and a dotless PHP file, even in a pool with `security.limit_extensions` emptied (under the first version, which sent the path as SCRIPT_FILENAME, that pool ran the file — reproduced before the fix). The no-dot rule stays as defence in depth (a dotless path never names a `.php`/`.phar` file). — If wrong: a pool whose status path contains a dot is read through the HTTP form instead.
- R4 Socket path: absolute, at most 103 bytes (macOS `sun_path`, the smallest agent platform; review fix M2), no `:` (the separator), no control characters, not starting with `//`. TCP: an IP literal (IPv6 in brackets) or a DNS name, and a port 1–65535, required (no implicit 9000). — Explicit beats a hidden default; the bound turns a runtime failure into a form error. — If wrong: an operator types the port, or shortens a socket path.
- R5 New format `phpfpmPool` = `statusURL` or a valid FastCGI entry, with Go/TS vectors in testdata/formats.json, an API phrase and en/de texts; `statusURL` stays as it is for nginx, apache and caddy.
- R6 FastCGI request: role RESPONDER, request id 1, no keep-conn; params exactly `GATEWAY_INTERFACE=CGI/1.1`, `REQUEST_METHOD=GET`, `SCRIPT_FILENAME=/` (fixed, never a regular file), `SCRIPT_NAME`, `DOCUMENT_URI` = the path, `REQUEST_URI` = path + `?json`, `QUERY_STRING=json`, `SERVER_PROTOCOL=HTTP/1.1`; empty STDIN. Bounds: CGI header block 16 KiB, body phpfpm's 1 MiB, STDERR 64 KiB (read and dropped), all raw bytes 2 × body bound + 128 KiB; dial and exchange under `dbcheck.QueryTimeout`, all pools under the round's `CollectTimeout`; the caller's context closes the connection.
- R7 Reasons: dial and I/O errors through `dbcheck.Classify` (early close = "connection failed"); the CGI `Status:` (absent = 200) maps to the same texts as HTTP (shared `webcheck.StatusReason`); a malformed record or header block is `unexpected response format`; END_REQUEST with a protocol status other than REQUEST_COMPLETE is "request rejected"; an oversize stream is "response too large"; a socket that does not exist or may not be opened is "socket not found" / "permission denied" (added during Task 3: the common misconfigurations deserve their own text).
- R8 The basic-auth secret applies to HTTP entries only (FastCGI has nowhere to carry it); a refused secret still fails every pool, as today. `verify_tls` does not apply to FastCGI entries.
- R9 The agent re-parses every FastCGI entry with the shared parser; one that does not parse (only possible past the manager's validation) reads down with "invalid address" and nothing is dialled.
- R10 Real-FPM coverage via the plugin's existing container test (Linux Docker only): TCP to the FPM container directly; the socket form through the in-process fake server (a container's socket is not reachable from the host on Docker Desktop).

## Review Focus

- A path that is not the status path must not run PHP, whatever the pool's `security.limit_extensions`: real FPM answers the same 404 for `/var/www/html/runme` (a dotless PHP file), `/nope` and `/etc/hostname`, in the default pool and in one with `security.limit_extensions` emptied, and the file never runs — `TestFastCGIAgainstARealServer` (subtest "a path that is not the status path runs nothing").
- Hostile or broken servers: padding, split STDOUT, STDERR noise, foreign request id, oversize, endless empty records, early close, a server that never answers — `fastcgi` client tests `TestGet*`.
- HTTP entries unchanged and mixed lists — existing phpfpm tests plus `TestMixedHTTPAndFastCGIPools`.
- Older-agent behaviour claim — `TestFetchRefusesFastCGIEntriesWithoutDialing` (webcheck).
- Grammar agreement Go/TS — formats.json vectors run by both sides.

---

### Task 1: Grammar and format (manager side)

**Files:** create `internal/checkplugins/phpfpm_pool.go` (+ `_test.go`); modify `formats.go`, `phpfpm.go`, `phpfpm_test.go`, `testdata/formats.json`, `internal/api/check_plugin_schema.go`, `web/src/test/checkPlugins.json` (regenerated).

```go
type PHPFPMFastCGI struct{ Network, Address, Path string } // "tcp"|"unix"
func IsPHPFPMFastCGI(s string) bool                       // prefix fcgi:// or unix:
func ParsePHPFPMFastCGI(s string) (PHPFPMFastCGI, error)  // error text = format problem
```
Tests: `TestParsePHPFPMFastCGI` (table), `TestValidatePHPFPMParams` new rows, vectors. Commit `feat(checkplugins): accept FastCGI addresses as phpfpm pools`.

### Task 2: FastCGI client

**Files:** create `internal/agent/checks/plugins/internal/fastcgi/{fastcgi.go,record.go,fastcgi_test.go}`, `fastcgi/fastcgitest/fastcgitest.go`.

```go
type Response struct{ Status int; Body []byte }
var ErrTooLarge, ErrProtocol, ErrRejected error
func Get(ctx context.Context, network, address, path, query string, maxBody int) (Response, error)
```
Tests `TestGet*`: framing and padding, multi-record STDOUT, STDERR dropped, Status header, LF-only headers, missing header end, foreign request id, unknown record type, oversize body/stderr/raw, empty-record flood, early close, timeout, cancel, unix socket, exact params sent. Commit `feat(agent): minimal FastCGI client for status pages`.

### Task 3: phpfpm reads FastCGI pools

**Files:** modify `webcheck/round.go` (`RoundEach`, `Round` on top), `webcheck/webcheck.go` (`StatusReason`), `webcheck/webcheck_test.go`; create `phpfpm/fetch.go`, `phpfpm/fastcgi_test.go`; modify `phpfpm/phpfpm.go`, `phpfpm/integration_test.go`.

Tests: `TestFastCGIPoolOverTCP`, `TestFastCGIPoolOverSocket`, `TestMixedHTTPAndFastCGIPools`, `TestFastCGIFailuresReportThePoolDown`, `TestFastCGIIgnoresTheSecret`, `TestFastCGIUnparsableEntry`, `TestFastCGIAgainstARealServer`, `TestFastCGIUnknownPathIsNotRun`. Commit `feat(agent): phpfpm reads pools over FastCGI directly`.

### Task 4: SPA (folded into Task 1: the shared vectors make the TS twin part of the same green commit)

**Files:** `web/src/features/checks/paramFormats.ts` (`phpfpmPool`), `web/src/i18n/{en,de}/checks.ts` (format text, urls label/hint), `CheckDialog.webserver.test.tsx`. Commit `feat(web): phpfpm pools accept FastCGI addresses`.

### Task 5: Docs

`docs/BACKLOG.md` 3.11a done, `docs/upgrades.md` (R2). Commit `docs: backlog 3.11a done`.
