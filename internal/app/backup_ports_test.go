package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/backup"
)

type fakeBackupAdmins struct {
	emails []string
	err    error
}

func (f fakeBackupAdmins) InstanceAdminEmails(context.Context) ([]string, error) {
	return f.emails, f.err
}

type fakeBackupSMTP struct {
	configured    bool
	configuredErr error
	sendErr       error
	sent          [][]string
	subject, body string
}

func (f *fakeBackupSMTP) Configured(context.Context) (bool, error) {
	return f.configured, f.configuredErr
}

func (f *fakeBackupSMTP) SendMany(_ context.Context, to []string, subject, body string) error {
	f.sent = append(f.sent, to)
	f.subject, f.body = subject, body
	return f.sendErr
}

func TestBackupAdminMailer(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	smtp := &fakeBackupSMTP{configured: true}
	m := backupAdminMailer{admins: fakeBackupAdmins{emails: []string{"a@example.com", "b@example.com"}}, smtp: smtp}
	if err := m.SendToInstanceAdmins(ctx, "Pulsix backup failed", "body"); err != nil {
		t.Fatal(err)
	}
	if len(smtp.sent) != 1 || !slices.Equal(smtp.sent[0], []string{"a@example.com", "b@example.com"}) || smtp.subject != "Pulsix backup failed" || smtp.body != "body" {
		t.Errorf("sent %v %q %q, want one mail to both admins", smtp.sent, smtp.subject, smtp.body)
	}

	for _, tc := range []struct {
		name    string
		admins  fakeBackupAdmins
		smtp    *fakeBackupSMTP
		wantErr error
	}{
		{"mail not configured", fakeBackupAdmins{emails: []string{"a@example.com"}}, &fakeBackupSMTP{}, nil},
		{"no administrators", fakeBackupAdmins{}, &fakeBackupSMTP{configured: true}, nil},
		{"settings unreadable", fakeBackupAdmins{emails: []string{"a@example.com"}}, &fakeBackupSMTP{configuredErr: boom}, boom},
		{"admins unreadable", fakeBackupAdmins{err: boom}, &fakeBackupSMTP{configured: true}, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := backupAdminMailer{admins: tc.admins, smtp: tc.smtp}.SendToInstanceAdmins(ctx, "s", "b")
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if len(tc.smtp.sent) != 0 {
				t.Errorf("sent %v, want nothing", tc.smtp.sent)
			}
		})
	}

	smtp = &fakeBackupSMTP{configured: true, sendErr: boom}
	if err := (backupAdminMailer{admins: fakeBackupAdmins{emails: []string{"a@example.com"}}, smtp: smtp}).SendToInstanceAdmins(ctx, "s", "b"); !errors.Is(err, boom) {
		t.Errorf("send failure = %v, want it passed on", err)
	}
}

// TestAppPoolSetsApplicationName: restore refuses while a manager is
// connected, and recognizes one by its sessions' application_name.
func TestAppPoolSetsApplicationName(t *testing.T) {
	ctx := context.Background()
	a, err := Build(ctx, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var name string
	if err := a.db.Pool().QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE pid = pg_backend_pid()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "pulsix-manager" {
		t.Errorf("application_name = %q, want pulsix-manager", name)
	}
}

// TestBuildWiresBackups: the manager builds the backup service, which
// works against the app pool, and Close stops it.
func TestBuildWiresBackups(t *testing.T) {
	ctx := context.Background()
	a, err := Build(ctx, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.backups == nil {
		t.Fatal("no backup service")
	}
	set, err := a.backups.Settings(ctx)
	if err != nil || set != backup.DefaultSettings() {
		t.Errorf("settings = %+v, %v; want the defaults", set, err)
	}
	next, dest, err := a.backups.Status(ctx)
	if err != nil || next != nil || dest != backup.DefaultDir {
		t.Errorf("status = %v, %q, %v", next, dest, err)
	}
	a.Close()
}
