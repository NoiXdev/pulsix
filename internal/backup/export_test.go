package backup

import "github.com/jackc/pgx/v5/pgxpool"

// SetAfterMigrate installs a hook Restore runs on the target pool right after
// MigrateTo and before the load, so a test can add a table the migrations do
// not create.
func SetAfterMigrate(o *RestoreOptions, fn func(*pgxpool.Pool) error) { o.afterMigrate = fn }
