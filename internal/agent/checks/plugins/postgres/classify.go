package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// classify maps an error from connecting or querying to one fixed text. It
// never returns any part of err: pgx's connect errors quote the user and
// host, a server's message can quote anything. Only the five-character
// SQLSTATE of a server error is kept, and only when it is well formed.
func classify(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		code := pe.Code
		switch {
		case !validSQLState(code):
			return "server error"
		case strings.HasPrefix(code, "28"):
			return "authentication failed (SQLSTATE " + code + ")"
		case code == "42501":
			return "permission denied (SQLSTATE " + code + ")"
		default:
			return "server error (SQLSTATE " + code + ")"
		}
	}
	// pgconn's fixed refusal when sslmode needs TLS and the server has none;
	// matched, never echoed.
	if err != nil && strings.Contains(err.Error(), "server refused TLS connection") {
		return "TLS handshake failed"
	}
	return dbcheck.Classify(err)
}

func validSQLState(c string) bool {
	if len(c) != 5 {
		return false
	}
	for i := 0; i < 5; i++ {
		if (c[i] < '0' || c[i] > '9') && (c[i] < 'A' || c[i] > 'Z') {
			return false
		}
	}
	return true
}
