package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// TestItemTakenOffTheListClosesItsAlertStale is backlog 3.55's path,
// performed through its real cause: the operator takes the mount with the
// open alert off the disk check's list (here "/" — the rig's tuple — is
// replaced by "/data"). The check itself still resolves, so before 3.55
// the evaluator kept judging the frozen "/" value and the alert stayed
// open with nothing on the host page to show why. Now Load gives the
// tuple interval 0, Evaluate reports it Unmeasured, and
// alerts.Reconcile's staleActions closes the alert `stale` in the host's
// own transaction — the removed-check path's own close. The check_states
// row stays standing (that path never deletes it; the item expiry
// forgets the current row later), and the host roll-up no longer counts
// the tuple.
func TestItemTakenOffTheListClosesItsAlertStale(t *testing.T) {
	r := newLifecycleRig(t)
	a := r.openEpisode(t)

	if _, err := r.checks.UpdateDefinition(r.scopedCtx, r.org, r.diskDefID, checks.DefinitionInput{
		Key: checks.PluginDisk, Plugin: checks.PluginDisk,
		Params:   map[string]any{"mounts": []any{"/data"}},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	}); err != nil {
		t.Fatalf("take / off the disk check's list: %v", err)
	}
	r.advance(time.Minute)
	// The agent now reports the mount it is asked for, healthy.
	healthy := 10.0
	if err := gen.New(r.d.Pool()).UpsertCheckCurrent(context.Background(), gen.UpsertCheckCurrentParams{
		OrgID: r.org, HostID: r.host, CheckKey: checks.PluginDisk, Item: "/data", Metric: "disk.used_pct",
		Value: &healthy, At: r.now,
	}); err != nil {
		t.Fatalf("upsert /data: %v", err)
	}
	r.makeDue(t)
	r.tick(t)

	closed, reason := r.closure(t, a.ID)
	if !closed || reason != string(alerts.Stale) {
		t.Fatalf("after taking / off the list: closed=%v reason=%q, want closed with reason stale", closed, reason)
	}
	if !r.checkStateExists(t) {
		t.Fatal("the check_states row was deleted; the not-asked path must leave it standing like a removed check's")
	}
	var state string
	var crit, warn, unknown int
	if err := r.d.Pool().QueryRow(context.Background(),
		`SELECT state, crit_count, warn_count, unknown_count FROM check_host_states WHERE host_id = $1`, r.host).
		Scan(&state, &crit, &warn, &unknown); err != nil {
		t.Fatalf("read host roll-up: %v", err)
	}
	if state != "ok" || crit != 0 || warn != 0 || unknown != 0 {
		t.Fatalf("host roll-up = %s crit=%d warn=%d unknown=%d, want ok with nothing counted — / is no longer judged", state, crit, warn, unknown)
	}
}
