package mysql

import (
	"database/sql/driver"
	"errors"
	"strconv"

	"github.com/go-sql-driver/mysql"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// errUnexpectedResult is a server answer this plugin cannot read (a status
// row or a replication column missing).
var errUnexpectedResult = errors.New("unexpected result")

// classify maps an error from connecting or querying to one fixed text. It
// never returns any part of err: a server's message can quote the user, the
// host or a statement. Only the numeric error code of a server error is kept.
func classify(err error) string {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		n := strconv.Itoa(int(me.Number))
		switch me.Number {
		case 1044, 1045, 1130, 1698, 1820, 1862:
			return "authentication failed (error " + n + ")"
		case 1142, 1143, 1227, 1370:
			return "permission denied (error " + n + ")"
		case 1969, 3024:
			// The server's own statement bound (MariaDB max_statement_time,
			// MySQL MAX_EXECUTION_TIME) ended the query.
			return "timed out"
		default:
			return "server error (error " + n + ")"
		}
	}
	switch {
	case errors.Is(err, mysql.ErrNoTLS):
		return "TLS handshake failed"
	case errors.Is(err, mysql.ErrCleartextPassword), errors.Is(err, mysql.ErrNativePassword),
		errors.Is(err, mysql.ErrOldPassword), errors.Is(err, mysql.ErrUnknownPlugin):
		return "authentication failed"
	case errors.Is(err, errUnexpectedResult):
		return "unexpected server response"
	case errors.Is(err, mysql.ErrInvalidConn), errors.Is(err, driver.ErrBadConn):
		return "connection failed"
	}
	return dbcheck.Classify(err)
}
