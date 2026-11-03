package api_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/backup"
)

func TestBackupLifecycle(t *testing.T) {
	s := apitest.New(t)
	admin, adminID := backupAdmin(t, s)

	before := listBackups(t, s, admin)
	if len(before.Items) != 0 || before.NextRunAt != nil || before.StorageError != nil || before.DestinationLabel == "" {
		t.Fatalf("list on a fresh instance = %+v", before)
	}

	b := startBackup(t, s, admin)
	if b.State != "succeeded" || b.Kind != "manual" || b.SizeBytes <= 0 || b.PulsixVersion != "test" || b.Error != "" {
		t.Fatalf("backup entry = %+v", b)
	}
	if _, _, ok := backup.ParseObjectName(b.Name); !ok {
		t.Fatalf("backup name %q is not a backup object name", b.Name)
	}
	if b.ActorID == nil || *b.ActorID != adminID.String() || b.ActorName == "" {
		t.Fatalf("backup actor = %v / %q, want the admin", b.ActorID, b.ActorName)
	}
	if _, err := time.Parse(time.RFC3339, b.CreatedAt); err != nil {
		t.Fatalf("created_at %q: %v", b.CreatedAt, err)
	}

	// The download is the stored file, byte for byte.
	stored, err := os.ReadFile(filepath.Join(s.BackupDir, b.Name))
	if err != nil {
		t.Fatal(err)
	}
	resp := requestJSON(t, admin, http.MethodGet, s.HTTP.URL+backupsPath+"/"+b.Name+"/download", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(stored) {
		t.Fatalf("downloaded %d bytes, stored %d bytes: contents differ", len(got), len(stored))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="`+b.Name+`"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if resp.ContentLength != int64(len(stored)) {
		t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(stored))
	}

	// Names that are not a backup, or are not there, are plain 404s.
	for _, name := range []string{
		"..%2Fetc%2Fpasswd", "..%2F..%2Fetc%2Fpasswd", "%2Fetc%2Fpasswd", "pulsix-20200101T000000Z-manual.pxbak.tmp",
		"pulsix-20200101T000000Z-manual.pxbak", "pulsix-probe-0123.tmp", "x",
	} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			path := backupsPath + "/" + name
			if method == http.MethodGet {
				path += "/download"
			}
			if resp := requestJSON(t, admin, method, s.HTTP.URL+path, nil); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: status %d, want 404", method, path, resp.StatusCode)
			}
		}
	}

	if resp := requestJSON(t, admin, http.MethodDelete, s.HTTP.URL+backupsPath+"/"+b.Name, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if after := listBackups(t, s, admin); len(after.Items) != 0 {
		t.Fatalf("the deleted backup is still listed: %+v", after.Items)
	}
	if resp := requestJSON(t, admin, http.MethodDelete, s.HTTP.URL+backupsPath+"/"+b.Name, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete: status %d, want 404", resp.StatusCode)
	}

	events := myActivity(t, s, admin)
	for action, object := range map[string]string{
		"POST createBackup": "backup", "GET downloadBackup": "backup", "DELETE deleteBackup": "backup",
	} {
		ev := findEvent(t, events, action)
		if ev.OrgID != nil || ev.ObjectType != object || ev.ActorID == nil || *ev.ActorID != adminID.String() {
			t.Errorf("%s event = %+v, want an instance-level %s event by the admin", action, ev, object)
		}
		if action != "POST createBackup" && ev.ObjectID != b.Name {
			t.Errorf("%s object id = %q, want the backup name", action, ev.ObjectID)
		}
	}
}

func TestBackupListSurvivesAnUnavailableDestination(t *testing.T) {
	s := apitest.New(t)
	admin, _ := backupAdmin(t, s)
	failed := startBackup(t, s, admin)
	if failed.State != "succeeded" {
		t.Fatalf("setup backup = %+v", failed)
	}

	// The folder becomes a file: listing it fails, the attempt history is
	// still known.
	if err := os.RemoveAll(s.BackupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.BackupDir, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := listBackups(t, s, admin)
	if list.StorageError == nil || *list.StorageError == "" {
		t.Fatalf("storage_error = %v, want the reason the folder cannot be listed", list.StorageError)
	}
	if list.DestinationLabel == "" {
		t.Fatalf("destination_label is empty: %+v", list)
	}
}

func TestBackupNowIsRefusedWhileOneRuns(t *testing.T) {
	release := make(chan struct{})
	s := apitest.New(t, func(srv *apitest.Server) {
		srv.Deps.Backups = srv.NewBackups(srv.BackupDir, func(ctx context.Context, w io.Writer, opts backup.DumpOptions) (backup.Header, error) {
			select {
			case <-release:
			case <-ctx.Done():
				return backup.Header{}, ctx.Err()
			}
			return srv.DumpBackup(ctx, w, opts)
		})
	})
	admin, _ := backupAdmin(t, s)

	if resp := requestJSON(t, admin, http.MethodPost, s.HTTP.URL+backupsPath, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, admin, http.MethodPost, s.HTTP.URL+backupsPath, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second POST while the first runs: status %d, want 409; body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if list := listBackups(t, s, admin); len(list.Items) != 1 || list.Items[0].State != "running" {
		t.Fatalf("list while a backup runs = %+v", list.Items)
	}
	close(release)
	if end := awaitBackupEnd(t, s, admin); end.State != "succeeded" {
		t.Fatalf("the backup ended %+v", end)
	}
	// Only the accepted request is on the audit trail.
	n := 0
	for _, e := range myActivity(t, s, admin) {
		if e.Action == "POST createBackup" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d createBackup events, want 1", n)
	}
}
