package postgres

import "github.com/jackc/pgx/v5/pgconn"

func pgErr(code, msg string) error {
	return &pgconn.PgError{Severity: "FATAL", Code: code, Message: msg}
}
