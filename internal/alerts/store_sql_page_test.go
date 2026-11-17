package alerts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/paging"
	"github.com/noixdev/pulsix/internal/rules"
)

func TestSQLStoreListAlertsPageRejectsUnknownSort(t *testing.T) {
	d := testdb.Open(t)
	org, _ := seedHost(t, d)
	for _, sort := range []string{"mute", "", "opened_at"} {
		_, _, err := alerts.NewSQLStore(d).ListAlertsPage(context.Background(), org, alerts.Filter{}, alerts.MuteQuery{}, paging.Page{Number: 1, Size: 10, Sort: sort})
		if !errors.Is(err, alerts.ErrInvalidInput) {
			t.Errorf("sort %q: err = %v, want ErrInvalidInput", sort, err)
		}
	}
}

// TestSQLStoreMutedPredicate: the SQL decides muted from the snooze columns,
// the expiry instant it is handed and the host ids, and a closed alert is
// never muted whatever its columns say.
func TestSQLStoreMutedPredicate(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org, host := seedHost(t, d)
	other := seedAdditionalHost(t, d, org)
	st := alerts.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)

	open := func(h uuid.UUID, metric string) uuid.UUID {
		tx := beginTx(t, d)
		if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionOpen, Subject: tuple(h, "cpu", "", metric), At: now.Add(-time.Hour), Severity: rules.StateWarn}}); err != nil {
			t.Fatal(err)
		}
		list, err := st.OpenForHost(ctx, tx, org, h)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			if a.Subject.Metric == metric {
				return a.ID
			}
		}
		t.Fatal("alert not found")
		return uuid.Nil
	}
	future, expired, untilChange, onHost, plain := open(other, "future"), open(other, "expired"), open(other, "change"), open(host, "host"), open(other, "plain")
	for id, q := range map[uuid.UUID]string{
		future:      "UPDATE alerts SET snoozed_until = $2::timestamptz WHERE id = $1",
		expired:     "UPDATE alerts SET snoozed_until = $2::timestamptz - interval '2 hours' WHERE id = $1",
		untilChange: "UPDATE alerts SET snooze_until_change = true, snoozed_until = NULL WHERE $2::timestamptz IS NOT NULL AND id = $1",
	} {
		if _, err := d.Pool().Exec(ctx, q, id, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(muted bool, hostIDs ...uuid.UUID) map[uuid.UUID]bool {
		rows, total, err := st.ListAlertsPage(ctx, org, alerts.Filter{Muted: &muted}, alerts.MuteQuery{Now: now, HostIDs: hostIDs}, paging.Page{Number: 1, Size: 100, Sort: "opened", Desc: true})
		if err != nil || total != len(rows) {
			t.Fatalf("ListAlertsPage(muted=%v) = %d rows, total %d, %v", muted, len(rows), total, err)
		}
		m := map[uuid.UUID]bool{}
		for _, r := range rows {
			m[r.ID] = true
		}
		return m
	}
	got := ids(true, host)
	if len(got) != 3 || !got[future] || !got[untilChange] || !got[onHost] {
		t.Fatalf("muted=true = %v, want future, until-change and the host-window alert", got)
	}
	got = ids(false, host)
	if len(got) != 2 || !got[expired] || !got[plain] {
		t.Fatalf("muted=false = %v, want expired and plain", got)
	}
	// Close the host-window alert: it drops out of muted.
	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionClose, AlertID: onHost, At: now, Reason: alerts.Resolved}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ids(true, host); got[onHost] {
		t.Fatalf("a closed alert counts as muted: %v", got)
	}
}
