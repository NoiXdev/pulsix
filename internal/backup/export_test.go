package backup

import "github.com/jackc/pgx/v5/pgxpool"

// SetAfterMigrate installs a hook Restore runs on the target pool right after
// MigrateTo and before the load, so a test can add a table the migrations do
// not create.
func SetAfterMigrate(o *RestoreOptions, fn func(*pgxpool.Pool) error) { o.afterMigrate = fn }

// SetAfterWipe installs a hook Restore runs on the target pool right after
// the wipe and before MigrateTo, so a test can play a manager that migrates
// the wiped schema in between.
func SetAfterWipe(o *RestoreOptions, fn func(*pgxpool.Pool) error) { o.afterWipe = fn }

// WaitIdle waits until every backup StartManual began in the background has
// finished.
func WaitIdle(s *Service) { s.wg.Wait() }
