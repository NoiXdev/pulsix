package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/noixdev/pulsix/internal/backup"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
)

// managerApplicationName is the application_name of every session a running
// manager opens. restore refuses while a session of that name is connected.
const managerApplicationName = "pulsix-manager"

// backupAdmins lists who hears about a failed scheduled backup.
type backupAdmins interface {
	InstanceAdminEmails(ctx context.Context) ([]string, error)
}

// backupSMTP is the part of mail.SMTP the failure mail needs.
type backupSMTP interface {
	Configured(ctx context.Context) (bool, error)
	SendMany(ctx context.Context, to []string, subject, body string) error
}

// backupAdminMailer implements backup.AdminMailer: one mail to every
// enabled instance administrator. Without outbound mail settings, or
// without an administrator, it sends nothing.
type backupAdminMailer struct {
	admins backupAdmins
	smtp   backupSMTP
}

func (m backupAdminMailer) SendToInstanceAdmins(ctx context.Context, subject, body string) error {
	configured, err := m.smtp.Configured(ctx)
	if err != nil {
		return err
	}
	if !configured {
		return nil
	}
	to, err := m.admins.InstanceAdminEmails(ctx)
	if err != nil {
		return fmt.Errorf("list instance administrators: %w", err)
	}
	if len(to) == 0 {
		return nil
	}
	return m.smtp.SendMany(ctx, to, subject, body)
}

// newBackupService builds the backup service on the app pool: scheduled and
// manual backups dump through pulsix_app without an organization scope,
// which the org_isolation_bypass policies let read every organization.
func newBackupService(appDB *db.DB, box *crypto.Box, masterKey [32]byte, smtp backupSMTP, version string) *backup.Service {
	store := backup.NewSQLStore(appDB)
	dump := func(ctx context.Context, w io.Writer, opts backup.DumpOptions) (backup.Header, error) {
		return backup.Dump(ctx, appDB.Pool(), masterKey, w, opts)
	}
	return backup.NewService(store, box, dump, backup.StorageFor(backup.DefaultDir),
		backupAdminMailer{admins: store, smtp: smtp}, version, time.Now, slog.Default())
}
