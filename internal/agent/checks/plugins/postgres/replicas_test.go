package postgres

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

func f64(v float64) *float64 { return &v }

func TestReplicaLagOnThePrimary(t *testing.T) {
	sess := &fakeSession{
		stats: serverStats{MaxConnections: 10},
		replicas: []replica{
			{Name: "standby-a", Host: "10.0.0.5", State: "streaming", LagBytes: f64(4096), LagSeconds: f64(1.5)},
			{Name: "walreceiver", Host: "local", State: "catchup", LagBytes: f64(0), LagSeconds: f64(0)},
		},
	}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.replicas", 2)
	mustValue(t, obs, "replica:standby-a@10.0.0.5", "pg.replica_lag_bytes", 4096)
	mustValue(t, obs, "replica:standby-a@10.0.0.5", "pg.replica_lag_seconds", 1.5)
	mustLabel(t, obs, "replica:standby-a@10.0.0.5", "pg.replica_state", "streaming")
	mustValue(t, obs, "replica:walreceiver@local", "pg.replica_lag_bytes", 0)
	mustLabel(t, obs, "replica:walreceiver@local", "pg.replica_state", "catchup")
	if sess.gotReplicaLimit != maxReplicas {
		t.Errorf("replica limit = %d, want %d", sess.gotReplicaLimit, maxReplicas)
	}
	// The standby's own lag on item "" is unchanged: 0 on a primary.
	mustValue(t, obs, "", "pg.replication_lag_bytes", 0)
}

func TestNoReplicas(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.replicas", 0)
	for _, o := range obs {
		if strings.HasPrefix(o.Item, replicaItemPrefix) {
			t.Errorf("replica item without replicas: %+v", o)
		}
	}
}

// The time lag (R6 as amended): 0 while nothing is behind; otherwise the
// first of replay_lag, flush_lag, write_lag the server measured (the query
// coalesces them); none at all while bytes are behind leaves the metric out
// of the round. That is the window after an idle spell: PostgreSQL cleared
// the lag times, and the standby reports its replay position only on its
// next reply, so the byte lag reads > 0 for up to wal_receiver_status_interval.
func TestReplicaTimeLag(t *testing.T) {
	sess := &fakeSession{
		stats: serverStats{MaxConnections: 10},
		replicas: []replica{
			// Caught up: 0, whatever lag time the server still shows.
			{Name: "idle", Host: "10.0.0.1", State: "streaming", LagBytes: f64(0)},
			{Name: "settled", Host: "10.0.0.2", State: "streaming", LagBytes: f64(0), LagSeconds: f64(0.4)},
			// Behind with a measured lag time.
			{Name: "behind", Host: "10.0.0.3", State: "streaming", LagBytes: f64(100), LagSeconds: f64(2.5)},
			// The idle-to-write window: bytes behind, no lag time yet.
			{Name: "window", Host: "10.0.0.4", State: "streaming", LagBytes: f64(100)},
		},
	}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "replica:idle@10.0.0.1", "pg.replica_lag_seconds", 0)
	mustValue(t, obs, "replica:settled@10.0.0.2", "pg.replica_lag_seconds", 0)
	mustValue(t, obs, "replica:behind@10.0.0.3", "pg.replica_lag_seconds", 2.5)
	if o, ok := find(obs, "replica:window@10.0.0.4", "pg.replica_lag_seconds"); ok {
		t.Errorf("a time lag in the window: %+v", o)
	}
	mustValue(t, obs, "replica:window@10.0.0.4", "pg.replica_lag_bytes", 100)
	mustLabel(t, obs, "replica:window@10.0.0.4", "pg.replica_state", "streaming")
}

// The query reports only senders with a replay position, but a row without
// one is never read as 0 bytes behind.
func TestReplicaWithoutAReplayPositionIsNeverZero(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, replicas: []replica{{Name: "odd", Host: "h", State: "streaming", LagSeconds: f64(3)}}}
	obs, _ := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	mustErr(t, obs, "replica:odd@h", "pg.replica_lag_bytes", "postgres: replica reports no replay position")
	mustValue(t, obs, "replica:odd@h", "pg.replica_lag_seconds", 3)
}

// The sender filter lives in the query; pin what it leaves out.
func TestReplicasQueryFiltersNonReplicaSenders(t *testing.T) {
	for _, want := range []string{
		"state IS DISTINCT FROM 'backup'",
		"replay_lsn IS NOT NULL",
		"'pg_basebackup'", "'pg_receivewal'", "'barman_receive_wal'",
		`'_[0-9]+_sync_[0-9]+(_[0-9]+)?$'`,
		"COALESCE(replay_lag, flush_lag, write_lag)",
		"pg_last_wal_receive_lsn()",
	} {
		if !strings.Contains(replicasQuery, want) {
			t.Errorf("replicasQuery lacks %s", want)
		}
	}
	// The table-sync pattern: PostgreSQL 14+ names the worker's slot (its
	// application_name) pg_<subid>_sync_<relid>_<sysid>, 10 to 13
	// <slot>_<subid>_sync_<relid>; a subscription's apply sender (named
	// after the subscription) stays.
	re := regexp.MustCompile(`_[0-9]+_sync_[0-9]+(_[0-9]+)?$`)
	for name, sync := range map[string]bool{
		"pg_16394_sync_16385_7312345678901234567": true,
		"mysub_16394_sync_16385":                  true,
		"mysub":                                   false,
		"standby1":                                false,
		"walreceiver":                             false,
	} {
		if re.MatchString(name) != sync {
			t.Errorf("%q: table sync = %v, want %v", name, !sync, sync)
		}
	}
}

func TestReplicaWithoutANameIsKeyedByItsHost(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, replicas: []replica{{Host: "10.0.0.9", State: "streaming", LagBytes: f64(7), LagSeconds: f64(0)}}}
	obs, _ := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	mustValue(t, obs, "replica:10.0.0.9", "pg.replica_lag_bytes", 7)
}

func TestReplicaDuplicateKeyKeepsTheWorst(t *testing.T) {
	sess := &fakeSession{
		stats: serverStats{MaxConnections: 10},
		replicas: []replica{
			{Name: "walreceiver", Host: "10.0.0.5", State: "streaming", LagBytes: f64(10), LagSeconds: f64(0.1)},
			{Name: "walreceiver", Host: "10.0.0.5", State: "startup"},
			{Name: "walreceiver", Host: "10.0.0.5", State: "catchup", LagBytes: f64(9000), LagSeconds: f64(12)},
			{Name: "walreceiver", Host: "10.0.0.5", State: "streaming", LagBytes: f64(20), LagSeconds: f64(0.2)},
		},
	}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.replicas", 4)
	mustValue(t, obs, "replica:walreceiver@10.0.0.5", "pg.replica_lag_bytes", 9000)
	mustValue(t, obs, "replica:walreceiver@10.0.0.5", "pg.replica_lag_seconds", 12)
	mustLabel(t, obs, "replica:walreceiver@10.0.0.5", "pg.replica_state", "catchup")
	n := 0
	for _, o := range obs {
		if o.Item == "replica:walreceiver@10.0.0.5" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d observations on the shared key, want 3", n)
	}
}

func TestReplicasWithoutPrivilege(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10, LimitedView: true}, replicas: []replica{{Name: "x", Host: "h"}}}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustErr(t, obs, "", "pg.replicas", "postgres: no privilege to see replication")
	if sess.replicasQueried {
		t.Error("replicas queried without the privilege to see them")
	}
	for _, o := range obs {
		if strings.HasPrefix(o.Item, replicaItemPrefix) {
			t.Errorf("replica item without the privilege: %+v", o)
		}
	}
	mustValue(t, obs, "", "pg.up", 1)
}

// The replica read is an addition to the check: when it fails, only
// pg.replicas carries the error and the round stays up (like mysql's sizes).
func TestReplicaQueryFailureErrorsOnlyTheCount(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10, Connections: 3}, dbs: []database{{Name: "app", Size: i64(5)}}, replicasErr: errors.New("boom " + plantedHost)}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.up", 1)
	mustValue(t, obs, "", "pg.connections", 3)
	mustValue(t, obs, "app", "pg.database_size_bytes", 5)
	o, ok := find(obs, "", "pg.replicas")
	if !ok || o.Err == "" || o.Value != nil {
		t.Errorf("pg.replicas = %+v", o)
	}
	for _, o := range obs {
		if strings.HasPrefix(o.Item, replicaItemPrefix) {
			t.Errorf("replica item after a failed read: %+v", o)
		}
	}
	noSecretIn(t, obs, err)
}

func TestReplicaCapKeepsTheCountExact(t *testing.T) {
	var rs []replica
	for i := 0; i < maxReplicas+5; i++ {
		rs = append(rs, replica{Name: fmt.Sprintf("s%03d", i), Host: "h", State: "streaming", LagBytes: f64(0), LagSeconds: f64(0)})
	}
	total := 500
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, replicas: rs, replicasTotal: &total}
	obs, _ := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	mustValue(t, obs, "", "pg.replicas", 500)
	n := 0
	for _, o := range obs {
		if o.Metric == "pg.replica_state" {
			n++
		}
	}
	if n != maxReplicas {
		t.Errorf("%d replicas reported, want %d", n, maxReplicas)
	}
}

func TestUnreportableReplicaNameIsSkipped(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, replicas: []replica{
		{Name: "bad\nname", Host: "h", State: "streaming", LagBytes: f64(1), LagSeconds: f64(0)},
		{Name: "good", Host: "h", State: "streaming", LagBytes: f64(2), LagSeconds: f64(0)},
	}}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err == nil || !strings.Contains(err.Error(), "cannot be reported") {
		t.Errorf("err = %v", err)
	}
	for _, o := range obs {
		if !checks.ReportableItem(o.Item) {
			t.Errorf("unreportable item %q", o.Item)
		}
	}
	mustValue(t, obs, "replica:good@h", "pg.replica_lag_bytes", 2)
	mustValue(t, obs, "", "pg.replicas", 2)
}
