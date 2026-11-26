package postgres

import (
	"errors"
	"fmt"
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

func TestReplicaLagUnknownIsNeverZero(t *testing.T) {
	sess := &fakeSession{
		stats: serverStats{MaxConnections: 10},
		replicas: []replica{
			// Starting up: no replay position yet, no lag time.
			{Name: "starting", Host: "10.0.0.1", State: "startup"},
			// Bytes behind but no lag time measured.
			{Name: "behind", Host: "10.0.0.2", State: "streaming", LagBytes: f64(100)},
			// Caught up and idle: PostgreSQL clears the lag time.
			{Name: "idle", Host: "10.0.0.3", State: "streaming", LagBytes: f64(0)},
			// A lag time without a replay position.
			{Name: "timed", Host: "10.0.0.4", State: "streaming", LagSeconds: f64(3)},
		},
	}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustErr(t, obs, "replica:starting@10.0.0.1", "pg.replica_lag_bytes", "postgres: replica reports no replay position")
	mustErr(t, obs, "replica:starting@10.0.0.1", "pg.replica_lag_seconds", "postgres: replication lag time is unknown")
	mustValue(t, obs, "replica:behind@10.0.0.2", "pg.replica_lag_bytes", 100)
	mustErr(t, obs, "replica:behind@10.0.0.2", "pg.replica_lag_seconds", "postgres: replication lag time is unknown")
	mustValue(t, obs, "replica:idle@10.0.0.3", "pg.replica_lag_seconds", 0)
	mustErr(t, obs, "replica:timed@10.0.0.4", "pg.replica_lag_bytes", "postgres: replica reports no replay position")
	mustValue(t, obs, "replica:timed@10.0.0.4", "pg.replica_lag_seconds", 3)
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

func TestReplicaQueryFailureIsADownRound(t *testing.T) {
	sess := &fakeSession{stats: serverStats{MaxConnections: 10}, replicasErr: errors.New("boom " + plantedHost)}
	obs, err := collect(t, newTestPlugin(sess, nil, nil), nil, goodFields)
	if err != nil {
		t.Fatal(err)
	}
	mustValue(t, obs, "", "pg.up", 0)
	if o, ok := find(obs, "", "pg.replicas"); !ok || o.Err == "" {
		t.Errorf("pg.replicas on a failed round = %+v", o)
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
