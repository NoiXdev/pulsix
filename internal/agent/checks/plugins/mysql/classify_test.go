package mysql

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func myErr(n uint16, msg string) error { return &mysql.MySQLError{Number: n, Message: msg} }

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"access denied", myErr(1045, "Access denied for user '"+plantedUser+"'@'"+plantedHost+"' (using password: YES)"), "authentication failed (error 1045)"},
		{"db access denied", myErr(1044, "x"), "authentication failed (error 1044)"},
		{"host not allowed", myErr(1130, "Host '"+plantedHost+"' is not allowed to connect"), "authentication failed (error 1130)"},
		{"password expired", myErr(1820, "x"), "authentication failed (error 1820)"},
		{"password expired, no change allowed", myErr(1862, "x"), "authentication failed (error 1862)"},
		{"auth plugin", myErr(1698, "x"), "authentication failed (error 1698)"},
		{"command denied", myErr(1227, "Access denied; you need the PROCESS privilege"), "permission denied (error 1227)"},
		{"table denied", myErr(1142, "x"), "permission denied (error 1142)"},
		{"column denied", myErr(1143, "x"), "permission denied (error 1143)"},
		{"procedure denied", myErr(1370, "x"), "permission denied (error 1370)"},
		{"other server error", myErr(1040, "Too many connections"), "server error (error 1040)"},
		{"wrapped", fmt.Errorf("query: %w", myErr(1045, "x")), "authentication failed (error 1045)"},
		{"no TLS", mysql.ErrNoTLS, "TLS handshake failed"},
		{"cleartext", mysql.ErrCleartextPassword, "authentication failed"},
		{"native", mysql.ErrNativePassword, "authentication failed"},
		{"old", mysql.ErrOldPassword, "authentication failed"},
		{"unknown auth plugin", mysql.ErrUnknownPlugin, "authentication failed"},
		{"invalid conn", mysql.ErrInvalidConn, "connection failed"},
		{"bad conn", driver.ErrBadConn, "connection failed"},
		{"unexpected EOF", io.ErrUnexpectedEOF, "connection failed"},
		{"deadline", os.ErrDeadlineExceeded, "timed out"},
		{"unexpected result", errUnexpectedResult, "unexpected server response"},
		{"anything else", errors.New("dial tcp " + plantedHost + ": " + plantedPassword), "connection failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			if got != tc.want {
				t.Fatalf("classify = %q, want %q", got, tc.want)
			}
		})
	}
}
