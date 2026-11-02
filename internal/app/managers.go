package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// managerApplicationName is the application_name of every session a
	// running manager opens. restore refuses while a session of that name
	// is connected.
	managerApplicationName = "pulsix-manager"
	// cliApplicationName is the application_name of the backup and restore
	// commands' sessions, so they never count as a running manager.
	cliApplicationName = "pulsix-manager-cli"
)

// ManagersConnected counts the sessions a running manager holds on pool's
// database: those named managerApplicationName in pg_stat_activity, other
// than the caller's own. Every role may read another session's
// application_name there, so no extra privilege is needed.
func ManagersConnected(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_stat_activity
		WHERE datname = current_database() AND application_name = $1 AND pid <> pg_backend_pid()`,
		managerApplicationName).Scan(&n); err != nil {
		return 0, fmt.Errorf("look for running managers: %w", err)
	}
	return n, nil
}
