package redis

import (
	"crypto/tls"
	"errors"
	"net"
	"strings"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// settings is the validated, defaulted secret: where the server is and how
// to log in. It holds credentials, so nothing formats it.
type settings struct {
	// network and addr are what to dial: "tcp" and host:port, or "unix" and
	// the socket path.
	network, addr string
	// host is the TLS server name; it is not used with a socket.
	host           string
	user, password string
	tls            string
	// tlsCA, tlsCert and tlsKey are the TLS files' paths, serverName the
	// name the server's certificate is verified against (host when empty).
	tlsCA, tlsCert, tlsKey, serverName string
	// tlsConfig is built from the above by withTLS before the dial; nil
	// means plain TCP.
	tlsConfig *tls.Config
}

func noNUL(s string) bool { return !strings.ContainsRune(s, 0) }

// fieldSpec is the secret file format of the redis plugin (design: "Secret
// fields"). Unknown fields are refused by dbcheck.Parse. Every field is
// optional: no secret at all is a local server without authentication.
var fieldSpec = []dbcheck.Field{
	{Name: "host", Default: "127.0.0.1", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "port", Default: "6379", Valid: dbcheck.Port, Want: "a valid port"},
	{Name: "socket", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "user", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "password", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "tls", Default: "false", Valid: dbcheck.OneOf("false", "true", "skip-verify"), Want: "one of false, true, skip-verify"},
	{Name: "tls_ca", Valid: dbcheck.AbsPath, Want: "an absolute path"},
	{Name: "tls_cert", Valid: dbcheck.AbsPath, Want: "an absolute path"},
	{Name: "tls_key", Valid: dbcheck.AbsPath, Want: "an absolute path"},
	{Name: "tls_server_name", Valid: noNUL, Want: "text without NUL bytes"},
}

// parseSettings reads the secret's fields; nil or empty fields mean
// 127.0.0.1:6379 without authentication or TLS. A user needs a password: an
// ACL user is never logged in without one, and AUTH with the user alone
// would send the user as the password.
func parseSettings(fields map[string]string) (settings, error) {
	v, err := dbcheck.Parse(fields, fieldSpec)
	if err != nil {
		return settings{}, err
	}
	// One surrounding pair of brackets is an IPv6 literal written the URL
	// way; JoinHostPort adds its own.
	host := strings.TrimSuffix(strings.TrimPrefix(v["host"], "["), "]")
	s := settings{
		host: host, user: v["user"], password: v["password"], tls: v["tls"],
		tlsCA: v["tls_ca"], tlsCert: v["tls_cert"], tlsKey: v["tls_key"], serverName: v["tls_server_name"],
	}
	if s.user != "" && s.password == "" {
		return settings{}, errors.New("secret field `password` is required")
	}
	if err := tlsFieldRules(v); err != nil {
		return settings{}, err
	}
	if socket := v["socket"]; socket != "" {
		if s.tls != "false" {
			return settings{}, errors.New("secret field `tls` is not false for a socket")
		}
		s.network, s.addr = "unix", socket
	} else {
		s.network, s.addr = "tcp", net.JoinHostPort(s.host, v["port"])
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
			return errors.New("secret field `" + f + "` needs `tls` true or skip-verify")
		}
	}
	return nil
}

// withTLS reads the TLS files and returns s with its tls.Config: nil for
// tls false; verification against the CA file (the system roots without
// one) and serverName or host for true; none for skip-verify. A client
// certificate is sent in both TLS modes. Errors name a field, never a path.
func (s settings) withTLS() (settings, error) {
	if s.tls == "false" {
		s.tlsConfig = nil
		return s, nil
	}
	m, err := dbcheck.TLSFiles{
		CA: s.tlsCA, Cert: s.tlsCert, Key: s.tlsKey,
		CAField: "tls_ca", CertField: "tls_cert", KeyField: "tls_key",
	}.Load()
	if err != nil {
		return settings{}, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: m.Certificates}
	if s.tls == "skip-verify" {
		cfg.InsecureSkipVerify = true //nolint:gosec // the operator chose skip-verify in the secret
	} else {
		cfg.RootCAs = m.RootCAs
		cfg.ServerName = s.serverName
		if cfg.ServerName == "" {
			cfg.ServerName = s.host
		}
	}
	s.tlsConfig = cfg
	return s, nil
}
