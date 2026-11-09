package db

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// LatestVersion returns the version of the newest migration embedded in this
// binary: the schema version a database is at once Migrate has run. A
// restore compares a backup's schema version against it.
func LatestVersion() (int64, error) {
	return latestVersionIn(migrations)
}

// latestVersionIn is LatestVersion over the migrations directory of fsys.
func latestVersionIn(fsys fs.FS) (int64, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return 0, fmt.Errorf("list embedded migrations: %w", err)
	}
	var latest int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		v, err := goose.NumericComponent(e.Name())
		if err != nil {
			// Not a migration (the directory's README, say).
			continue
		}
		latest = max(latest, v)
	}
	if latest == 0 {
		return 0, errors.New("no embedded migrations")
	}
	return latest, nil
}
