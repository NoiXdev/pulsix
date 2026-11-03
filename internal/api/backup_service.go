package api

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

// BackupService is the part of backup.Service the instance backup routes
// call. *backup.Service satisfies it.
type BackupService interface {
	Settings(ctx context.Context) (backup.Settings, error)
	// SaveSettings stores in; a nil secretKey keeps the stored S3 secret
	// and an empty one removes it.
	SaveSettings(ctx context.Context, in backup.Settings, secretKey *string) error
	// Test probes the destination the given settings describe, saving
	// nothing.
	Test(ctx context.Context, in backup.Settings, secretKey *string) error
	// List returns the entries and, when the destination could not be
	// listed, an error wrapping backup.ErrStorageUnavailable alongside the
	// entries that are still known.
	List(ctx context.Context) ([]backup.Entry, error)
	StartManual(ctx context.Context, actor uuid.UUID) (uuid.UUID, error)
	Delete(ctx context.Context, name string) error
	Open(ctx context.Context, name string) (io.ReadCloser, int64, error)
	Status(ctx context.Context) (nextRun *time.Time, destination string, err error)
}
