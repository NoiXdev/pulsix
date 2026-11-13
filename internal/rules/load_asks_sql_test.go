package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestLoadLeavesItemsNobodyAsksForUnmeasured (backlog 3.55): a mount taken
// off a disk check's list keeps its check_current row, but nobody asks the
// agent for it any more, so Load gives its tuples interval 0 — the same
// "no resolved interval" path a removed check takes, out of the evaluation
// with no state and its open alerts closed. The item still listed keeps
// the check's interval; so does the whole-host item "", even when the
// predicate itself would say no (Load never consults it for ""); and a
// key without a predicate is asked for everything, as before.
func TestLoadLeavesItemsNobodyAsksForUnmeasured(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "asks")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	root := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	data := rules.Tuple{CheckKey: "disk", Item: "/data", Metric: "disk.used_pct"}
	dataFree := rules.Tuple{CheckKey: "disk", Item: "/data", Metric: "disk.free_bytes"}
	whole := rules.Tuple{CheckKey: "disk", Item: "", Metric: "disk.check"}
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	for _, tu := range []rules.Tuple{root, data, dataFree, whole, cpu} {
		insertCurrent(t, d, org, host, tu, ptr(50.0), nil, now.Add(-30*time.Second), "")
	}

	checks := fixedChecks{
		intervals: map[string]time.Duration{"disk": 5 * time.Minute, "cpu": time.Minute},
		asks: map[string]func(string) bool{
			"disk": func(item string) bool { return item == "/" },
		},
	}
	store := rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := map[rules.Tuple]time.Duration{}
	for _, ts := range load.Tuples {
		got[ts.Tuple] = ts.Interval
	}
	want := map[rules.Tuple]time.Duration{
		root:     5 * time.Minute,
		data:     0,
		dataFree: 0,
		whole:    5 * time.Minute,
		cpu:      time.Minute,
	}
	if len(got) != len(want) {
		t.Fatalf("tuples = %v, want every current row (none deleted)", got)
	}
	for tu, iv := range want {
		if got[tu] != iv {
			t.Errorf("interval of %+v = %v, want %v", tu, got[tu], iv)
		}
	}
}
