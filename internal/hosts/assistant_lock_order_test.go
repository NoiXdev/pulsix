package hosts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
)

// keyShareWatcher stands for the reviewer: while it is handed the change
// (where the reviewer locks the schedules and triggers), it asks for the
// host row FOR KEY SHARE without waiting — what a schedule tick's job
// insert takes on its target hosts.
type keyShareWatcher struct {
	d      *db.DB
	hostID uuid.UUID
	err    error
	called bool
}

func (w *keyShareWatcher) Changed(ctx context.Context, _ uuid.UUID, _ assistantchange.Change) error {
	w.called = true
	tx, err := w.d.Pool().Begin(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // read only
	var one int
	w.err = tx.QueryRow(context.WithoutCancel(ctx), `SELECT 1 FROM hosts WHERE id = $1 FOR KEY SHARE NOWAIT`, w.hostID).Scan(&one)
	return nil
}

// TestARenameLocksTheItemsBeforeTheHostRow (final review N2): an
// assistant's rename of a managed host hands its change on — where the
// reviewer locks the schedules and triggers — before the rename takes the
// host row's key lock, so a schedule tick that holds its rows while its
// job insert takes FOR KEY SHARE on the host is never stuck behind this
// write while this write waits on the tick's rows.
func TestARenameLocksTheItemsBeforeTheHostRow(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "hosts-rename-lock-order")
	svc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	scoped := db.WithOrgScope(context.Background(), org)
	zone, err := svc.CreateZone(scoped, org, "fra", "Falkenstein")
	if err != nil {
		t.Fatal(err)
	}
	h, err := svc.CreateHost(scoped, org, hosts.HostInput{Name: "web-01", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	w := &keyShareWatcher{d: d, hostID: h.ID}
	svc.SetChangeWatcher(w)
	assistant := assistantchange.Actor{UserID: uuid.Must(uuid.NewV7()), Assistant: true}
	if _, err := svc.UpdateHostBy(scoped, org, h.ID, h.Revision, hosts.HostInput{Name: "web-1", Address: "10.0.0.1", ZoneID: zone.ID, Managed: true}, assistant); err != nil {
		t.Fatal(err)
	}
	if !w.called || w.err != nil {
		t.Fatalf("while the change was handed on, a job insert's key share on the host = %v (called %v), want granted", w.err, w.called)
	}
}
