package postgres

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// connSettings is the validated, defaulted secret: where the server is and
// how to log in. It holds credentials, so nothing formats it.
type connSettings struct {
	host, port, user, password, database, sslmode, sslrootcert string
}

func noNUL(s string) bool { return !strings.ContainsRune(s, 0) }

// hostOK accepts one host name, address or Unix socket directory: no NUL
// byte and no comma (libpq's multi-host list).
func hostOK(s string) bool { return noNUL(s) && !strings.Contains(s, ",") }

// fieldSpec is the secret file format of the postgres plugin (design:
// "Secret fields"). Unknown fields are refused by dbcheck.Parse.
var fieldSpec = []dbcheck.Field{
	{Name: "host", Default: "localhost", Valid: hostOK, Want: "a single host name or a socket directory"},
	{Name: "port", Default: "5432", Valid: dbcheck.Port, Want: "a valid port"},
	{Name: "user", Required: true, Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "password", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "database", Default: "postgres", Valid: noNUL, Want: "text without NUL bytes"},
	{Name: "sslmode", Default: "prefer", Valid: dbcheck.OneOf("disable", "prefer", "require", "verify-ca", "verify-full"), Want: "one of disable, prefer, require, verify-ca, verify-full"},
	{Name: "sslrootcert", Valid: noNUL, Want: "text without NUL bytes"},
}

func parseSettings(fields map[string]string) (connSettings, error) {
	v, err := dbcheck.Parse(fields, fieldSpec)
	if err != nil {
		return connSettings{}, err
	}
	return connSettings{
		host: v["host"], port: v["port"], user: v["user"], password: v["password"],
		database: v["database"], sslmode: v["sslmode"], sslrootcert: v["sslrootcert"],
	}, nil
}

// errRootCert is a CA file the agent cannot read.
var errRootCert = errors.New("sslrootcert cannot be read")

// checkRootCert reports whether the CA file named by sslrootcert (when it
// names a file) can be opened. It never returns the path.
func checkRootCert(s connSettings) error {
	if s.sslrootcert == "" || s.sslrootcert == "system" {
		return nil
	}
	f, err := os.Open(s.sslrootcert)
	if err != nil {
		return errRootCert
	}
	_ = f.Close()
	return nil
}

// errInvalidSettings is the only error buildConfig returns: pgx's own parse
// errors quote the (redacted, but still partly echoed) connection settings.
var errInvalidSettings = errors.New("invalid connection settings")

const (
	applicationName = "pulsix-agent"
	// statementTimeoutMS is dbcheck.QueryTimeout for the server's
	// statement_timeout.
	statementTimeoutMS = "5000"
)

// quote renders v as a libpq keyword/value value: single-quoted, with
// backslash and single quote escaped, so no value can end the quoting or
// add a keyword.
func quote(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' || v[i] == '\'' {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	b.WriteByte('\'')
	return b.String()
}

// buildConfig turns the settings into a pgx connection configuration that
// depends on nothing but them.
//
// pgx offers no way to build a pgconn.Config without ParseConfig, which is
// also where sslmode and the CA file become a tls.Config. ParseConfig
// merges three layers: built-in defaults (files under the agent's home
// directory: ~/.pgpass, ~/.postgresql/*), then the PG* environment
// variables, then the connection string. The string is assembled here
// with every value quoted (quote) and every key that matters set
// explicitly, empty ones included: a key present in the string, even
// empty, replaces whatever the environment or a default would supply
// (libpq's own rule, kept by pgx). Afterwards the runtime parameters are
// replaced wholesale, so PGOPTIONS and PGTZ cannot reach the server.
//
// The one input this cannot shadow is PGSERVICE: a set service makes
// ParseConfig read the service file, and the string's keys win over the
// file's, so the result stays the same unless that service or file is
// missing, which fails the round as "connection failed".
func buildConfig(s connSettings) (*pgx.ConnConfig, error) {
	kv := [][2]string{
		{"host", s.host},
		{"port", s.port},
		{"user", s.user},
		{"password", s.password},
		{"dbname", s.database},
		{"sslmode", s.sslmode},
		{"sslrootcert", s.sslrootcert},
		{"sslcert", ""},
		{"sslkey", ""},
		{"sslcertmode", "disable"},
		{"sslpassword", ""},
		{"sslnegotiation", "postgres"},
		{"sslsni", "1"},
		{"passfile", ""},
		{"target_session_attrs", "any"},
		{"channel_binding", "prefer"},
		{"require_auth", ""},
		{"min_protocol_version", ""},
		{"max_protocol_version", ""},
		{"application_name", applicationName},
		{"connect_timeout", strconv.Itoa(int(dbcheck.CollectTimeout.Seconds()))},
		{"statement_timeout", statementTimeoutMS},
	}
	var b strings.Builder
	for i, p := range kv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(quote(p[1]))
	}
	cfg, err := pgx.ParseConfig(b.String())
	if err != nil {
		return nil, errInvalidSettings
	}
	cfg.RuntimeParams = map[string]string{
		"application_name":  applicationName,
		"statement_timeout": statementTimeoutMS,
	}
	// One plain round trip per statement, no prepared statements, so nothing
	// is left on the server. Behind a pooler such as PgBouncer the
	// statement_timeout startup parameter above is refused unless the pooler
	// lists it in ignore_startup_parameters.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	return cfg, nil
}
