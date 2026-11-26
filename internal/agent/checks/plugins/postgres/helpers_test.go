package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// plantedPassword and plantedUser are values the tests plant in the secret
// and in driver errors; no observation or advisory error may carry them.
const (
	plantedPassword = "p4ss-w0rd-SECRET"
	plantedUser     = "planted-user-name"
	plantedHost     = "planted-host.example"
)

// fakeSession answers from fixed rows.
type fakeSession struct {
	stats     serverStats
	statsErr  error
	dbs       []database
	total     *int // overrides the matched count; nil means len(dbs)
	dbsErr    error
	gotNames  []string
	gotLimit  int
	closed    bool
	dbQueried bool

	replicas        []replica
	replicasTotal   *int // overrides the matched count; nil means len(replicas)
	replicasErr     error
	gotReplicaLimit int
	replicasQueried bool
}

func (f *fakeSession) Server(context.Context) (serverStats, error) { return f.stats, f.statsErr }

func (f *fakeSession) Databases(_ context.Context, names []string, limit int) ([]database, int, error) {
	f.dbQueried = true
	f.gotNames, f.gotLimit = names, limit
	if f.dbsErr != nil {
		return nil, 0, f.dbsErr
	}
	out := f.dbs
	if len(out) > limit {
		out = out[:limit]
	}
	// total is what the query matches before its LIMIT, like count(*) OVER ().
	if f.total != nil {
		return out, *f.total, nil
	}
	return out, len(f.dbs), nil
}

func (f *fakeSession) Replicas(_ context.Context, limit int) ([]replica, int, error) {
	f.replicasQueried = true
	f.gotReplicaLimit = limit
	if f.replicasErr != nil {
		return nil, 0, f.replicasErr
	}
	out := f.replicas
	if len(out) > limit {
		out = out[:limit]
	}
	if f.replicasTotal != nil {
		return out, *f.replicasTotal, nil
	}
	return out, len(f.replicas), nil
}

func (f *fakeSession) Close() { f.closed = true }

// newTestPlugin returns a plugin whose connect hands out sess, or fails
// with connErr; got receives the parsed connection settings.
func newTestPlugin(sess *fakeSession, connErr error, got *connSettings) plugin {
	return plugin{connect: func(_ context.Context, c connSettings) (session, error) {
		if got != nil {
			*got = c
		}
		if connErr != nil {
			return nil, connErr
		}
		return sess, nil
	}}
}

var goodFields = map[string]string{"user": "mon", "password": plantedPassword}

func collect(t *testing.T, p plugin, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	t.Helper()
	return p.CollectWithSecret(context.Background(), params, fields)
}

// find returns the observation of item/metric.
func find(obs []checks.Observation, item, metric string) (checks.Observation, bool) {
	for _, o := range obs {
		if o.Item == item && o.Metric == metric {
			return o, true
		}
	}
	return checks.Observation{}, false
}

func mustValue(t *testing.T, obs []checks.Observation, item, metric string, want float64) {
	t.Helper()
	o, ok := find(obs, item, metric)
	if !ok {
		t.Fatalf("no observation %q/%q in %+v", item, metric, obs)
	}
	if o.Value == nil || *o.Value != want {
		t.Fatalf("%q/%q = %+v, want value %v", item, metric, o, want)
	}
}

func mustLabel(t *testing.T, obs []checks.Observation, item, metric, want string) {
	t.Helper()
	o, ok := find(obs, item, metric)
	if !ok {
		t.Fatalf("no observation %q/%q in %+v", item, metric, obs)
	}
	if o.Label == nil || *o.Label != want || o.Value != nil {
		t.Fatalf("%q/%q = %+v, want label %q", item, metric, o, want)
	}
}

func mustErr(t *testing.T, obs []checks.Observation, item, metric, want string) {
	t.Helper()
	o, ok := find(obs, item, metric)
	if !ok {
		t.Fatalf("no observation %q/%q in %+v", item, metric, obs)
	}
	if o.Value != nil || o.Label != nil || o.Err != want {
		t.Fatalf("%q/%q = %+v, want error %q", item, metric, o, want)
	}
}

func noSecretIn(t *testing.T, obs []checks.Observation, err error) {
	t.Helper()
	var all []string
	for _, o := range obs {
		all = append(all, o.Item, o.Metric, o.Err)
		if o.Label != nil {
			all = append(all, *o.Label)
		}
	}
	if err != nil {
		all = append(all, err.Error())
	}
	text := strings.Join(all, "\n")
	for _, s := range []string{plantedPassword, plantedUser, plantedHost} {
		if strings.Contains(text, s) {
			t.Fatalf("%q leaked into %q", s, text)
		}
	}
}
