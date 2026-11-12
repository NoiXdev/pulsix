package redis

import (
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
	s := settings{host: host, user: v["user"], password: v["password"], tls: v["tls"]}
	if s.user != "" && s.password == "" {
		return settings{}, errors.New("secret field `password` is required")
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
