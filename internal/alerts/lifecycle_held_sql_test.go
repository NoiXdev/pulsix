// Backlog 3.9b ruling through the real evaluation stack: a metric its
// descriptor holds on its item's state (a sleeping disk's sector count,
// an exported pool's capacity) keeps its state and its alert — open,
// unchanged, the host's roll-up still at its severity — however long the
// item stays in that state, and the same alert continues once the item
// reports again. A metric not asked for another reason (its item taken
// off the check's list) still closes stale, as before.
package alerts_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// heldRig is lifecycleRig for one plugin whose items can wait on a state:
// one monitored host, one organization-bound check of plugin at a
// one-minute interval and one crit rule on one of its metrics.
type heldRig struct {
	d         *db.DB
	org, host uuid.UUID
	checks    *checks.Service
	ticker    *rules.Ticker
	now       time.Time
	scopedCtx context.Context //nolint:containedctx // the fixture's org-bound context
	plugin    string
	defID     uuid.UUID
	subject   alerts.Subject
}

func newHeldRig(t *testing.T, plugin string, params map[string]any, item, metric string, threshold float64) *heldRig {
	t.Helper()
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	if _, err := d.Pool().Exec(context.Background(), `UPDATE hosts SET monitored = true WHERE id = $1`, host); err != nil {
		t.Fatalf("mark host monitored: %v", err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksSvc := checks.NewService(checks.NewSQLStore(d),
		checks.WithMatcher(app.NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(app.NewChecksHostRoster(hostsSvc)))
	rulesSvc := rules.NewService(rules.NewSQLStore(d))
	r := &heldRig{
		d: d, org: org, host: host, checks: checksSvc, plugin: plugin,
		now:     time.Now().UTC().Truncate(time.Microsecond),
		subject: alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: plugin, Item: item, Metric: metric},
	}
	clock := func() time.Time { return r.now }
	r.ticker = rules.NewTicker(
		rules.NewEvalSQLStore(d, app.NewRulesHostChecks(checksSvc), items.NewSQLStore(d), rules.AgentOnlineWindow{Floor: agents.OnlineWindowFloor, HeartbeatsPerWindow: agents.HeartbeatsPerOnlineWindow}, quiet),
		alerts.NewService(alerts.NewSQLStore(d), clock),
		rules.WithTickerClock(clock), rules.WithTickerLogger(quiet),
	)
	r.scopedCtx = db.WithOrgScope(context.Background(), org)
	def, err := checksSvc.CreateDefinition(r.scopedCtx, org, checks.DefinitionInput{
		Key: plugin, Plugin: plugin, Params: params,
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	if err != nil {
		t.Fatalf("create %s definition: %v", plugin, err)
	}
	r.defID = def.ID
	if _, err := rulesSvc.Create(r.scopedCtx, org, rules.RuleInput{
		Name: metric + " high", Enabled: true, Scope: rules.ScopeOrg,
		Metric: metric, Severity: rules.SeverityCrit, Mode: "form",
		Form: &rules.Form{Metric: metric, Op: ">", Value: threshold},
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	return r
}

// reading is one check_current row the rig writes.
type reading struct {
	metric string
	value  *float64
	label  *string
}

func num(metric string, v float64) reading      { return reading{metric: metric, value: &v} }
func lbl(metric string, l string) reading       { return reading{metric: metric, label: &l} }
func (r *heldRig) advance(by time.Duration)     { r.now = r.now.Add(by) }
func (r *heldRig) tupleOf(metric string) string { return r.subject.Item + " " + metric }

// report writes the readings at the rig's now and marks the host dirty,
// as the ingest transaction does.
func (r *heldRig) report(t *testing.T, rs ...reading) {
	t.Helper()
	q := gen.New(r.d.Pool())
	for _, x := range rs {
		if err := q.UpsertCheckCurrent(context.Background(), gen.UpsertCheckCurrentParams{
			OrgID: r.org, HostID: r.host, CheckKey: r.plugin, Item: r.subject.Item, Metric: x.metric,
			Value: x.value, Label: x.label, At: r.now,
		}); err != nil {
			t.Fatalf("upsert %s: %v", r.tupleOf(x.metric), err)
		}
	}
	r.makeDue(t)
}

func (r *heldRig) makeDue(t *testing.T) {
	t.Helper()
	if err := gen.New(r.d.Pool()).MarkHostDirty(context.Background(), gen.MarkHostDirtyParams{OrgID: r.org, HostID: r.host}); err != nil {
		t.Fatalf("mark host dirty: %v", err)
	}
}

func (r *heldRig) tick(t *testing.T) {
	t.Helper()
	if _, err := r.ticker.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// alertsOnSubject is every alert, open or closed, ever raised on the
// rig's subject, oldest first.
func (r *heldRig) alertsOnSubject(t *testing.T) []struct {
	id       uuid.UUID
	severity string
	closed   bool
	reason   *string
} {
	t.Helper()
	rows, err := r.d.Pool().Query(context.Background(), `
		SELECT id, severity, closed_at IS NOT NULL, closed_reason FROM alerts
		WHERE host_id = $1 AND scope = 'tuple' AND check_key = $2 AND item = $3 AND metric = $4
		ORDER BY opened_at, id`, r.host, r.subject.CheckKey, r.subject.Item, r.subject.Metric)
	if err != nil {
		t.Fatalf("read alerts: %v", err)
	}
	defer rows.Close()
	var out []struct {
		id       uuid.UUID
		severity string
		closed   bool
		reason   *string
	}
	for rows.Next() {
		var a struct {
			id       uuid.UUID
			severity string
			closed   bool
			reason   *string
		}
		if err := rows.Scan(&a.id, &a.severity, &a.closed, &a.reason); err != nil {
			t.Fatalf("scan alert: %v", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read alerts: %v", err)
	}
	return out
}

// oneOpenAlert asserts that the subject has had exactly one alert, still
// open at crit, and returns its id.
func (r *heldRig) oneOpenAlert(t *testing.T, when string) uuid.UUID {
	t.Helper()
	got := r.alertsOnSubject(t)
	if len(got) != 1 || got[0].closed || got[0].severity != string(rules.StateCrit) {
		t.Fatalf("%s: alerts on %s = %+v, want one, open at crit", when, r.tupleOf(r.subject.Metric), got)
	}
	return got[0].id
}

// hostState is the roll-up the evaluation stored.
func (r *heldRig) hostState(t *testing.T) string {
	t.Helper()
	var state string
	if err := r.d.Pool().QueryRow(context.Background(),
		`SELECT state FROM check_host_states WHERE host_id = $1`, r.host).Scan(&state); err != nil {
		t.Fatalf("read roll-up: %v", err)
	}
	return state
}

// heldRow is the subject's stored state row: its evaluated_at and whether
// it is marked unmeasured.
func (r *heldRig) heldRow(t *testing.T) (evaluatedAt time.Time, marked bool) {
	t.Helper()
	if err := r.d.Pool().QueryRow(context.Background(), `
		SELECT evaluated_at, unmeasured_at IS NOT NULL FROM check_states
		WHERE host_id = $1 AND check_key = $2 AND item = $3 AND metric = $4`,
		r.host, r.subject.CheckKey, r.subject.Item, r.subject.Metric).Scan(&evaluatedAt, &marked); err != nil {
		t.Fatalf("read state row: %v", err)
	}
	return evaluatedAt, marked
}

// holdAcrossAWeek opens the crit alert with the item in its normal state,
// switches the item into its holding state and keeps it there for a week
// of evaluations, asserting that nothing about the alert, the roll-up or
// the stored row moves; then it wakes the item with the same reading and
// asserts the same alert carries on.
func (r *heldRig) holdAcrossAWeek(t *testing.T, normal, holding reading, value reading) {
	t.Helper()
	r.report(t, normal, value)
	r.tick(t)
	id := r.oneOpenAlert(t, "opened")
	judgedAt, _ := r.heldRow(t)

	for _, step := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour, 7 * 24 * time.Hour} {
		r.advance(step)
		r.report(t, holding)
		r.tick(t)
		if got := r.oneOpenAlert(t, "held for "+step.String()); got != id {
			t.Fatalf("held: the open alert is %s, want %s", got, id)
		}
		if got := r.hostState(t); got != string(rules.StateCrit) {
			t.Fatalf("held: roll-up = %s, want crit on the held state", got)
		}
		if evaluatedAt, marked := r.heldRow(t); !evaluatedAt.Equal(judgedAt) || marked {
			t.Fatalf("held: state row evaluated %v marked %v, want untouched since %v", evaluatedAt, marked, judgedAt)
		}
	}

	r.advance(time.Minute)
	r.report(t, normal, value)
	r.tick(t)
	if got := r.oneOpenAlert(t, "back"); got != id {
		t.Fatalf("back: the open alert is %s, want the same %s to continue", got, id)
	}
	if evaluatedAt, _ := r.heldRow(t); !evaluatedAt.Equal(r.now) {
		t.Fatalf("back: state row evaluated %v, want judged again at %v", evaluatedAt, r.now)
	}
}

// (a) A sleeping disk keeps its crit sector count and the alert on it.
func TestASleepingDiskKeepsItsAlert(t *testing.T) {
	r := newHeldRig(t, checks.PluginSmart, nil, "/dev/sdd", "smart.reallocated_sectors", 0)
	r.holdAcrossAWeek(t,
		lbl("smart.power_mode", checkplugins.SmartPowerModeActive),
		lbl("smart.power_mode", checkplugins.SmartPowerModeStandby),
		num("smart.reallocated_sectors", 3))
}

// (b) An exported pool keeps its crit capacity and the alert on it.
func TestAnExportedPoolKeepsItsAlert(t *testing.T) {
	r := newHeldRig(t, checks.PluginZFS, map[string]any{"optional_pools": []any{"backup"}}, "backup", "zfs.capacity_pct", 90)
	r.holdAcrossAWeek(t,
		lbl("zfs.state", "ONLINE"),
		lbl("zfs.state", checkplugins.ZFSStateExported),
		num("zfs.capacity_pct", 95))
}

// (d) A held disk the operator takes off the check's devices list is no
// longer held but unasked: its alert closes stale as before.
func TestASleepingDiskTakenOffTheListClosesItsAlertStale(t *testing.T) {
	r := newHeldRig(t, checks.PluginSmart, map[string]any{"devices": []any{"/dev/sdd"}}, "/dev/sdd", "smart.reallocated_sectors", 0)
	r.report(t, lbl("smart.power_mode", checkplugins.SmartPowerModeActive), num("smart.reallocated_sectors", 3))
	r.tick(t)
	id := r.oneOpenAlert(t, "opened")
	r.advance(time.Minute)
	r.report(t, lbl("smart.power_mode", checkplugins.SmartPowerModeStandby))
	r.tick(t)
	r.oneOpenAlert(t, "asleep")

	if _, err := r.checks.UpdateDefinition(r.scopedCtx, r.org, r.defID, checks.DefinitionInput{
		Key: r.plugin, Plugin: r.plugin, Params: map[string]any{"devices": []any{"/dev/sda"}},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	}); err != nil {
		t.Fatalf("take the disk off the list: %v", err)
	}
	r.advance(time.Minute)
	r.makeDue(t)
	r.tick(t)
	got := r.alertsOnSubject(t)
	if len(got) != 1 || got[0].id != id || !got[0].closed || got[0].reason == nil || *got[0].reason != string(alerts.Stale) {
		t.Fatalf("alerts after the disk left the list = %+v, want %s closed stale", got, id)
	}
}
