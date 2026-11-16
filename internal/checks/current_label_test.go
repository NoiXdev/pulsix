package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestCurrentLabelReadsOneTupleOnTheCallersTransaction is the alert
// detail's read (backlog 3.13b): one tuple's discrete label, on a
// transaction the caller owns — unscoped, like the evaluator's — so it sees
// that transaction's own uncommitted writes and nothing of another
// organization's.
func TestCurrentLabelReadsOneTupleOnTheCallersTransaction(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-current-label")
	other := seedOrg(t, d, "checks-current-label-other")
	host := seedHost(t, d, org, "label-host")
	now := time.Now().UTC().Truncate(time.Second)
	st := checks.NewSQLStore(d)
	svc := checks.NewService(st, fixedClock(now))
	if _, err := svc.RecordObservations(db.WithOrgScope(ctx, org), org, []checks.Observation{
		{HostID: host, CheckKey: "web", Metric: "http.failure", At: now, Label: str("timeout")},
		{HostID: host, CheckKey: "web", Metric: "http.up", At: now, Value: f64(0)},
		{HostID: host, CheckKey: "web", Item: "home", Metric: "http.assertion_actual", At: now, Err: "page is too complex to parse"},
		{HostID: host, CheckKey: "dns", Metric: "dns.failure", At: now, Label: str("servfail")},
	}); err != nil {
		t.Fatal(err)
	}

	tx, err := d.Pool().Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // test cleanup
	if _, err := tx.Exec(ctx, `UPDATE check_current SET label = 'nxdomain' WHERE host_id = $1 AND metric = 'dns.failure'`, host); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name                   string
		org                    uuid.UUID
		checkKey, item, metric string
		want                   string
	}{
		{"a label", org, "web", "", "http.failure", "timeout"},
		{"the transaction's own write", org, "dns", "", "dns.failure", "nxdomain"},
		{"a number", org, "web", "", "http.up", ""},
		{"an error", org, "web", "home", "http.assertion_actual", ""},
		{"no row", org, "web", "", "http.status", ""},
		{"another organization", other, "web", "", "http.failure", ""},
	} {
		got, err := st.CurrentLabel(ctx, tx, tc.org, host, tc.checkKey, tc.item, tc.metric)
		if err != nil || got != tc.want {
			t.Errorf("%s: CurrentLabel = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}
