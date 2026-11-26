package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// plantedPassword and plantedUser are values the tests plant in the secret
// and in driver errors; no observation or advisory error may carry them.
const (
	plantedPassword = "p4ss-w0rd-SECRET"
	plantedUser     = "planted-user-name"
	plantedHost     = "planted-host.example"
)

// fakeSession answers from fixed values.
type fakeSession struct {
	stats    serverStats
	statsErr error
	closed   bool

	schemas        []schemaSize
	schemasTotal   *int // overrides the matched count; nil means len(schemas)
	schemasErr     error
	schemasQueried bool
}

func (f *fakeSession) Server(context.Context) (serverStats, error) { return f.stats, f.statsErr }
func (f *fakeSession) Close()                                      { f.closed = true }

// Schemas returns at most maxSchemas rows, like the query's LIMIT.
func (f *fakeSession) Schemas(context.Context) ([]schemaSize, int, error) {
	f.schemasQueried = true
	if f.schemasErr != nil {
		return nil, 0, f.schemasErr
	}
	out := f.schemas
	if len(out) > maxSchemas {
		out = out[:maxSchemas]
	}
	if f.schemasTotal != nil {
		return out, *f.schemasTotal, nil
	}
	return out, len(f.schemas), nil
}

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// newTestPlugin returns a plugin whose connect hands out sess, or fails
// with connErr; got receives the parsed connection settings.
func newTestPlugin(sess *fakeSession, connErr error, got *connSettings) *plugin {
	return &plugin{
		now: time.Now,
		connect: func(_ context.Context, c connSettings) (session, error) {
			if got != nil {
				*got = c
			}
			if connErr != nil {
				return nil, connErr
			}
			return sess, nil
		},
	}
}

var goodFields = map[string]string{"user": "mon", "password": plantedPassword}

func collect(t *testing.T, p *plugin, fields map[string]string) ([]checks.Observation, error) {
	t.Helper()
	return p.CollectWithSecret(context.Background(), nil, fields)
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
	if o.Label == nil || *o.Label != want || o.Value != nil || o.Err != "" {
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

func f64(v float64) *float64 { return &v }
