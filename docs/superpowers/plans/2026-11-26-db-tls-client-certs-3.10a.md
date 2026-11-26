# Client certificates, custom CAs and Windows named pipes for the database checks (backlog 3.10a) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog row 3.10a (`grep -n "^| 3.10a " docs/BACKLOG.md`).

**Spec (the backlog row):** "Client certificates and custom CAs for the `mysql` and `redis` checks; Windows named pipes — `postgres` can verify against a CA file (`sslrootcert`), `mysql` and `redis` only use the system roots (`tls=true`) or skip verification (`skip-verify`), and neither offers a client certificate. Unix sockets work on Linux and macOS only; a Windows server listening on a named pipe is unreachable. Split from 3.10". Builds on `docs/superpowers/specs/2026-11-12-database-checks-design.md` ("Secret fields").

**Architecture:** agent only. The shared package `internal/agent/checks/plugins/internal/dbcheck` gains `TLSFiles` (`tlsfiles.go`): it validates and loads a CA file and a client certificate/key pair named by secret fields, with fixed error texts. `mysql` and `redis` gain the fields `tls_ca`, `tls_cert`, `tls_key`, `tls_server_name` and build their `tls.Config` in code; `postgres` gains `sslcert`/`sslkey` and runs its `sslrootcert` through the same loader. `mysql` gains a Windows-only `pipe` field dialed through go-winio (build tags, like the docker plugin's `dial_windows.go`). No manager, SPA, protocol or migration change: secrets live in `agent.yaml` only.

## Global Constraints
- Go layers per `CLAUDE.md`; `go test -race -timeout 900s ./internal/agent/checks/plugins/...`, never module-wide, never `make test`, never `internal/e2e`; `golangci-lint run` (also with `GOOS=windows`), `GOOS=windows go vet` and `gofmt -s -l` clean on touched packages before each commit.
- No error text carries a path, file content, host, user or password (the planted-value tests of 3.10 extend to the new fields).
- Real servers only through testcontainers (`containertest.RequireLinuxDocker`); throwaway PKI generated in the test.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no push, tag or merge.

## Rulings
- R1 **Field names:** `mysql` and `redis` get `tls_ca`, `tls_cert`, `tls_key`, `tls_server_name` (both plugins already speak `tls`); `postgres` gets libpq's `sslcert` and `sslkey` next to its `sslrootcert`, so each plugin keeps one vocabulary — cost if wrong: renaming a field is a loud "is not known" for anyone who used the old name.
- R2 **File fields are absolute paths**, read on every round (a rotated certificate is picked up without an agent restart): a regular file of at most 1 MiB holding PEM. Errors name the field and a fixed reason ("cannot be read", "is not a regular file", "is larger than 1 MiB", "is not a CA file", "is not a certificate file", "is not a private key file", "does not match `<cert field>`") — never the path or content. `sslrootcert` gets the absolute-path rule too (`system` stays) — a relative path resolved against the service's working directory, which differs between systemd, launchd and the Windows service — cost if wrong: an rc user with a relative `sslrootcert` gets "is not an absolute path" after the upgrade (noted in `docs/upgrades.md`).
- R3 **`tls_ca` and `tls_server_name` need `tls: true`**: they only matter when the server is verified, and silently ignoring a CA under `preferred` or `skip-verify` would suggest a verification that does not happen; refused as "secret field `tls_ca` needs `tls` true". With `tls: true` and no `tls_ca` the system roots stay. `postgres` keeps libpq's semantics unchanged (`sslrootcert` with `require` verifies the chain) — cost if wrong: an operator wanting "verify if a CA is given" must write `tls: true`.
- R4 **Server name** for verification: `tls_server_name` when set, else `host` (brackets stripped; an IP is verified against the certificate's IP SANs). For a `mysql` socket or pipe with `tls: true` that is the `host` field's value (default `127.0.0.1`) unless `tls_server_name` names one — before, the driver could not derive a name from a socket path and every such handshake failed — cost if wrong: none for working setups.
- R5 **Client certificate and key come as a pair** (one without the other is "secret field `tls_cert` needs `tls_key`" and vice versa) and need TLS (`tls` not `false`; `sslmode` not `disable`). They are sent under `preferred`/`skip-verify`/`prefer` too: client authentication is independent of verifying the server. The certificate file may hold a chain; the key file's first PEM block must be the key (PKCS#1, PKCS#8 or SEC 1 EC — pgx reads only the first block, so one rule for all three plugins) — cost if wrong: a combined "cert then key" file must be split.
- R6 **Key file permissions (Unix):** a key file with any permission bit for others (`mode & 0o007`) is refused ("secret field `tls_key` is accessible to other users; remove their permissions, for example chmod 600"); group read is allowed so the usual `0640 root:ssl-cert` layout works with the agent in that group (libpq is stricter for user-owned keys; the agent cannot know which group layout the operator chose). Windows: no check (ACLs, not mode bits) — cost if wrong: a 0644 key must be chmodded; the error says how.
- R7 **Encrypted keys are refused** ("secret field `tls_key` is an encrypted key, which is not supported; store it unencrypted with owner-only permissions"): PKCS#8 `ENCRYPTED PRIVATE KEY` and legacy `Proc-Type: 4,ENCRYPTED` blocks. A passphrase would have to sit in the same `agent.yaml` the agent reads, which protects nothing — cost if wrong: an operator must decrypt the key once (`openssl pkey`).
- R8 **TLS built in code, never through the driver's global registry**: `mysql` sets `Config.TLS` (a `*tls.Config`; `preferred` keeps `AllowFallbackToPlaintext` with verification off, as the driver does) instead of a `TLSConfig` name, so no process-wide state and no name collisions between checks; `redis` builds its `tls.Config` before the dial. Minimum TLS 1.2 everywhere — cost if wrong: none; behaviour without the new fields is unchanged.
- R9 **`mysql` named pipes:** a Windows-only `pipe` field, a pipe name (`MySQL`) or the local path `\\.\pipe\<name>`; remote pipes (`\\host\pipe\…`, SMB authentication) are refused; `pipe` with `socket` is refused; `host`/`port` are ignored like with a socket. Dialed with `winio.DialPipeContext` through `mysql.Config.DialFunc`, selected by build tags; a non-Windows agent refuses the field at parse ("secret field `pipe` is only available on Windows") — cost if wrong: none, the field is new.
- R10 **No named pipes for `redis` and `postgres`:** Redis has no Windows build; the archived Microsoft port (3.x) is below the plugin's Redis 5 floor, and Windows-native Redis-compatible servers (Memurai) and WSL/container setups listen on TCP. PostgreSQL on Windows has no named-pipe listener — cost if wrong: a later field, additive.
- R11 **A TLS alert received from the server** (Go: `*net.OpError` with `Op == "remote error"`, e.g. "certificate required" when no client certificate was sent) classifies as "TLS handshake failed" in `dbcheck.Classify`, not "connection failed" — cost if wrong: one more message class.
- R12 **Skew:** agent-local only. An older agent refuses a secret with a new field ("secret field `tls_ca` is not known"), loudly, on that check only. No manager or protocol change.
- R13 **Real-server tests:** each plugin's testcontainer test gains a TLS-with-client-certificate case (server requires a client certificate signed by a throwaway CA): `redis:7.4-alpine` with `--tls-auth-clients yes`, `mysql:8.4` with a `REQUIRE X509` user, `postgres` with a `hostssl … cert` rule. The PKI comes from a test-only helper package `plugins/internal/testpki` (CA, server and client certificates, PEM files written with chosen modes). Named pipes are covered by parse/selection tests on every OS and a go-winio listener test that only runs on Windows.

- R14 **Fix round 1 (review M1–M6):** (M1) `mysql`/`redis` skip leading `EC PARAMETERS` blocks in a key file (`openssl ecparam -genkey` output); `postgres` keeps the first-block rule (`TLSFiles.FirstBlockOnly`, pgx reads only that block) and a key after other blocks reads "does not start with the private key; remove the PEM blocks before it (such as EC PARAMETERS)". (M2) a TLS file is opened once (non-blocking on Unix, so a FIFO cannot hang the round) and the regular-file, size and permission checks run on the opened descriptor (`fstat`). (M3) no code change: any local user can create a pipe under the server's name while MySQL is down, and under `tls: preferred` the impostor gets the password (the driver's RSA exchange with the impostor's key); the user docs recommend `tls: true` with `tls_ca` for `pipe`. (M4) `postgres` refuses `sslcert`/`sslkey` with a socket-directory `host` ("needs a TCP host, not a socket directory"; pgx and libpq use no TLS over a socket). (M5) `sslrootcert: system` is pinned by a test. (M6) Windows key ACLs stay unchecked (R6); the user docs say so. The relative-`sslrootcert` break stays (controller ruling).

## Review Focus
- Error texts never carry the path or content of a file field (`TestTLSFilesErrorsNeverCarryThePath`, each plugin's `…NameTheFieldNeverTheValue`).
- Encrypted, mismatched, wrong-type and world-readable keys (`TestTLSFilesKey*`).
- Pair and mode rules (`TestParseSettingsTLSFieldRules` in mysql/redis, `TestParseSettingsClientCert` in postgres).
- A custom CA verifies and the server name is honoured; an unknown CA is still refused (redis `TestTLSWithACustomCA`, mysql `TestTLSConfig`).
- Client certificate actually sent; a server requiring one without it reads "TLS handshake failed" (redis `TestTLSClientCertificate`, integration cases).
- Key files from `openssl ecparam -genkey` and FIFOs (`TestTLSFilesKeyWithECParameters`, `TestTLSFilesFIFODoesNotHang`, postgres `TestKeyWithECParametersIsNamed`); postgres socket host with a client certificate (`TestBadSecretFields`), `system` (`TestRootCertSystem`).
- Pipe: non-Windows refusal, name forms, remote refusal, dialer selection (`TestParseSettingsPipe`, `TestBuildConfigPipe`, Windows-only `TestDialPipe`).

---

## Task 1: dbcheck — TLS files and the received alert
**Files:** `plugins/internal/dbcheck/tlsfiles.go`, `keyperm_unix.go`, `keyperm_windows.go`, `tlsfiles_test.go`, `dbcheck.go` (`isTLS`, `AbsPath`), `plugins/internal/testpki/testpki.go`.
Interface: `type TLSFiles struct{ CA, Cert, Key, CAField, CertField, KeyField string }`, `func (TLSFiles) Load() (TLSMaterial, error)`, `type TLSMaterial struct{ RootCAs *x509.CertPool; Certificates []tls.Certificate }`, `func AbsPath(string) bool`.
Tests: `TestTLSFilesLoad`, `TestTLSFilesNeedsBothCertAndKey`, `TestTLSFilesKeyEncrypted`, `TestTLSFilesKeyMismatch`, `TestTLSFilesKeyNotAKey`, `TestTLSFilesKeyPermissions` (Unix), `TestTLSFilesNotRegularOrTooLarge`, `TestTLSFilesErrorsNeverCarryThePath`, `TestClassify` (remote alert row).
- [ ] Commit `feat(agent): shared loader for database TLS files`.

## Task 2: postgres — client certificate
**Files:** `postgres/config.go` (`sslcert`, `sslkey`, absolute paths, `sslcertmode`), `postgres/postgres.go` (loader errors), tests, `integration_test.go` (cert auth case).
Tests: `TestParseSettingsClientCert`, `TestBuildConfigClientCert`, `TestUnreadableRootCertIsNamedWithoutThePath` (kept), `TestAgainstARealServerWithAClientCertificate`.
- [ ] Commit `feat(agent): postgres check sends a client certificate`.

## Task 3: redis — CA file, server name, client certificate
**Files:** `redis/config.go`, `redis/client.go` (`dial` takes the built `*tls.Config`), `redis/redis.go`, tests, `integration_test.go`.
Tests: `TestParseSettingsTLSFieldRules`, `TestTLSWithACustomCA`, `TestTLSServerName`, `TestTLSClientCertificate`, `TestAgainstARealServerWithAClientCertificate`.
- [ ] Commit `feat(agent): redis check verifies against a CA file and sends a client certificate`.

## Task 4: mysql — CA file, server name, client certificate, named pipe
**Files:** `mysql/config.go`, `mysql/tls.go`, `mysql/pipe_windows.go`, `mysql/pipe_other.go`, `mysql/session.go`, `mysql/mysql.go`, tests (`pipe_windows_test.go`), `integration_test.go`.
Tests: `TestParseSettingsTLSFieldRules`, `TestTLSConfig`, `TestParseSettingsPipe`, `TestBuildConfigPipe`, `TestDialPipe` (Windows), `TestAgainstARealServerWithAClientCertificate`.
- [ ] Commit `feat(agent): mysql check verifies against a CA file, sends a client certificate and reads a named pipe`.

## Task 5: Docs
`docs/BACKLOG.md` row 3.10a → done; the 3.10 spec's secret field table; `docs/upgrades.md` (relative `sslrootcert`, older agent refuses new fields).
- [ ] Commit `docs: backlog 3.10a done`.
