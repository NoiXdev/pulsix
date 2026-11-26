package mysql

import (
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// connSettings is the validated, defaulted secret: where the server is and
// how to log in. It holds credentials, so nothing formats it.
type connSettings struct {
	host, port, socket, user, password, tls string
	// pipe is a Windows named pipe's full path (\\.\pipe\<name>); when set,
	// like socket, host and port are not used.
	pipe string
	// tlsCA, tlsCert and tlsKey are the TLS files' paths, serverName the
	// name the server's certificate is verified against (host when empty).
	tlsCA, tlsCert, tlsKey, serverName string
	// tlsConfig is built from the above by withTLS before the connect; nil
	// means no TLS.
	tlsConfig *tls.Config
	// queryTimeout is set by the plugin, never read from the secret.
	queryTimeout time.Duration
}

func noNUL(s string) bool { return !strings.ContainsRune(s, 0) }

// fieldSpec is the secret file format of the mysql plugin (design: "Secret
// fields"). Unknown fields are refused by dbcheck.Parse.
var fieldSpec = []dbcheck.Field{
	{Name: "host", Default: "127.0.0.1", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "port", Default: "3306", Valid: dbcheck.Port, Want: "a valid port"},
	{Name: "socket", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "user", Required: true, Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "password", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "tls", Default: "preferred", Valid: dbcheck.OneOf("false", "preferred", "true", "skip-verify"), Want: "one of false, preferred, true, skip-verify"},
	{Name: "tls_ca", Valid: dbcheck.AbsPath, Want: "an absolute path"},
	{Name: "tls_cert", Valid: dbcheck.AbsPath, Want: "an absolute path"},
	{Name: "tls_key", Valid: dbcheck.AbsPath, Want: "an absolute path"},
	{Name: "tls_server_name", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "pipe", Valid: func(s string) bool { _, ok := pipePath(s); return ok }, Want: `a local pipe name such as MySQL or \\.\pipe\MySQL`},
}

// pipePrefix starts the path of a pipe on the local machine.
const pipePrefix = `\\.\pipe\`

// pipePath turns a pipe name or a local pipe path (prefix case-insensitive)
// into the path to dial. A name is 1 to 256 characters without a backslash
// or NUL byte, so a remote pipe (\\host\pipe\…) is refused.
func pipePath(s string) (string, bool) {
	name := s
	if len(s) >= len(pipePrefix) && strings.EqualFold(s[:len(pipePrefix)], pipePrefix) {
		name = s[len(pipePrefix):]
	}
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\\\x00") {
		return "", false
	}
	return pipePrefix + name, true
}

func parseSettings(fields map[string]string) (connSettings, error) {
	v, err := dbcheck.Parse(fields, fieldSpec)
	if err != nil {
		return connSettings{}, err
	}
	s := connSettings{
		host: v["host"], port: v["port"], socket: v["socket"],
		user: v["user"], password: v["password"], tls: v["tls"],
		tlsCA: v["tls_ca"], tlsCert: v["tls_cert"], tlsKey: v["tls_key"], serverName: v["tls_server_name"],
	}
	if err := tlsFieldRules(v); err != nil {
		return connSettings{}, err
	}
	if v["pipe"] != "" {
		if s.socket != "" {
			return connSettings{}, errors.New("secret field `pipe` cannot be combined with `socket`")
		}
		if !pipesSupported {
			return connSettings{}, errors.New("secret field `pipe` is only available on Windows")
		}
		s.pipe, _ = pipePath(v["pipe"])
	}
	return s, nil
}

// tlsFieldRules refuses TLS fields that would have no effect: a CA file
// and a server name only matter when the server is verified (tls true), a
// client certificate only over TLS.
func tlsFieldRules(v map[string]string) error {
	for _, f := range []string{"tls_ca", "tls_server_name"} {
		if v[f] != "" && v["tls"] != "true" {
			return errors.New("secret field `" + f + "` needs `tls` true")
		}
	}
	for _, f := range []string{"tls_cert", "tls_key"} {
		if v[f] != "" && v["tls"] == "false" {
			return errors.New("secret field `" + f + "` needs `tls` other than false")
		}
	}
	return nil
}

// withTLS reads the TLS files and returns s with its tls.Config: nil for
// false; no verification for preferred (which also falls back to plain,
// see buildConfig) and skip-verify; for true, verification against the CA
// file (the system roots without one) and serverName or host. A client
// certificate is sent in every TLS mode. Errors name a field, never a path.
func (s connSettings) withTLS() (connSettings, error) {
	if s.tls == "false" {
		s.tlsConfig = nil
		return s, nil
	}
	m, err := dbcheck.TLSFiles{
		CA: s.tlsCA, Cert: s.tlsCert, Key: s.tlsKey,
		CAField: "tls_ca", CertField: "tls_cert", KeyField: "tls_key",
	}.Load()
	if err != nil {
		return connSettings{}, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: m.Certificates}
	if s.tls == "true" {
		cfg.RootCAs = m.RootCAs
		cfg.ServerName = s.serverName
		if cfg.ServerName == "" {
			cfg.ServerName = bareHost(s.host)
		}
	} else {
		cfg.InsecureSkipVerify = true //nolint:gosec // preferred and skip-verify are the operator's choice in the secret
	}
	s.tlsConfig = cfg
	return s, nil
}

// bareHost strips one pair of brackets around an IPv6 literal.
func bareHost(h string) string { return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]") }

// errInvalidSettings is the only error buildConfig returns: the driver's own
// configuration errors can quote what it was given.
var errInvalidSettings = errors.New("invalid connection settings")

// buildConfig turns the settings into a driver configuration by setting its
// fields, never by formatting or parsing a DSN, so no value can inject an
// option. No ReadTimeout or WriteTimeout is set: the driver would report one
// as an invalid connection and hide that the server was too slow; the round
// and per-statement contexts bound every wait instead. TLS is the
// tls.Config withTLS built, never a name in the driver's process-wide
// registry; preferred keeps the driver's fallback to plain. A pipe is
// dialed by dialPipe (Windows only). The driver's logger
// is replaced by a silent one in this configuration only: it would
// otherwise print server details to stderr.
// AllowCleartextPasswords, AllowOldPasswords and AllowAllFiles stay at the
// driver's default, false.
func buildConfig(s connSettings) *mysql.Config {
	cfg := mysql.NewConfig()
	cfg.User = s.user
	cfg.Passwd = s.password
	switch {
	case s.pipe != "":
		cfg.Net, cfg.Addr, cfg.DialFunc = "pipe", s.pipe, dialPipe
	case s.socket != "":
		cfg.Net, cfg.Addr = "unix", s.socket
	default:
		cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(bareHost(s.host), s.port)
	}
	cfg.TLS = s.tlsConfig
	cfg.AllowFallbackToPlaintext = s.tlsConfig != nil && s.tls == "preferred"
	cfg.Timeout = dbcheck.CollectTimeout
	cfg.ParseTime = false
	cfg.AllowNativePasswords = true
	cfg.Logger = &mysql.NopLogger{}
	return cfg
}
