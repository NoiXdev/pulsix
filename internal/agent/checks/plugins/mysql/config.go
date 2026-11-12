package mysql

import (
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
}

func parseSettings(fields map[string]string) (connSettings, error) {
	v, err := dbcheck.Parse(fields, fieldSpec)
	if err != nil {
		return connSettings{}, err
	}
	return connSettings{
		host: v["host"], port: v["port"], socket: v["socket"],
		user: v["user"], password: v["password"], tls: v["tls"],
	}, nil
}

// errInvalidSettings is the only error buildConfig returns: the driver's own
// configuration errors can quote what it was given.
var errInvalidSettings = errors.New("invalid connection settings")

// buildConfig turns the settings into a driver configuration by setting its
// fields, never by formatting or parsing a DSN, so no value can inject an
// option. No ReadTimeout or WriteTimeout is set: the driver would report one
// as an invalid connection and hide that the server was too slow; the round
// and per-statement contexts bound every wait instead. TLS is one of the driver's built-in names; no custom CA is
// registered. The driver's logger is replaced by a silent one in this
// configuration only: it would otherwise print server details to stderr.
// AllowCleartextPasswords, AllowOldPasswords and AllowAllFiles stay at the
// driver's default, false.
func buildConfig(s connSettings) *mysql.Config {
	cfg := mysql.NewConfig()
	cfg.User = s.user
	cfg.Passwd = s.password
	if s.socket != "" {
		cfg.Net, cfg.Addr = "unix", s.socket
	} else {
		cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(s.host, "["), "]"), s.port)
	}
	cfg.TLSConfig = s.tls
	cfg.Timeout = dbcheck.CollectTimeout
	cfg.ParseTime = false
	cfg.AllowNativePasswords = true
	cfg.Logger = &mysql.NopLogger{}
	return cfg
}
