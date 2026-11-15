package probes

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
)

// snmpCheck is an snmp check naming secret, with one oids entry.
func snmpCheck(key, secret string, interval time.Duration) checks.Resolved {
	return checks.Resolved{Key: key, Plugin: "snmp", Interval: interval, Params: map[string]any{
		"key": key, "secret": secret, "oids": []any{"name=1.3.6.1.2.1.1.5.0"},
	}}
}

// vaultDescriptor is a stateless server plugin that takes a secret; no
// real one does yet, so the tests describe it themselves.
func vaultDescriptor(name string) (checkplugins.Descriptor, bool) {
	if name != "vault" {
		return checkplugins.Get(name)
	}
	return checkplugins.Descriptor{
		Name: "vault", Runs: checkplugins.Server,
		Params:  []checkplugins.ParamSpec{checkplugins.SecretParam()},
		Metrics: []checkplugins.MetricSpec{{Name: "vault.up"}, {Name: "vault.sealed"}},
	}, true
}

var swFields = map[string]string{"community": "s3cret-community"}

// TestSecretReachesThePoller: an snmp check's secret is looked up in its
// organization's scope and handed to the poll as its own argument; the
// params stay as they were.
func TestSecretReachesThePoller(t *testing.T) {
	h := newHarness(t, nil)
	h.secrets.set(h.org, "core-sw", swFields)
	check := snmpCheck("sw", "core-sw", time.Minute)
	want := maps.Clone(check.Params)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, check)
	start := time.Now()
	h.tickAt(0)
	after := time.Now()

	ps := h.pollers.Built()
	if len(ps) != 1 || len(ps[0].Calls()) != 1 {
		t.Fatalf("want one poller polled once, got %d pollers", len(ps))
	}
	c := ps[0].Calls()[0]
	if !maps.Equal(c.secret, swFields) {
		t.Fatalf("secret = %v, want the stored fields", c.secret)
	}
	if !reflect.DeepEqual(c.params, want) || c.address != "192.0.2.1" {
		t.Fatalf("params = %v, address %q; want the check's params unchanged and the host's address", c.params, c.address)
	}
	if l := h.secrets.Lookups(); len(l) != 1 || l[0].org != h.org || !l[0].scoped || l[0].scope != h.org || l[0].name != "core-sw" {
		t.Fatalf("lookups = %+v, want one for core-sw in the organization's scope", l)
	}
	// The lookup runs under the probe's deadline (interval - 1 s), so a
	// hung store cannot pin the slot in flight.
	if l := h.secrets.Lookups()[0]; !l.hasDeadline ||
		l.deadline.Before(start.Add(59*time.Second)) || l.deadline.After(after.Add(59*time.Second)) {
		t.Fatalf("lookup deadline %v (set %v), want start + interval - 1s (%v .. %v)",
			l.deadline, l.hasDeadline, start.Add(59*time.Second), after.Add(59*time.Second))
	}
	if n := len(h.probe.Calls()); n != 0 {
		t.Fatalf("the stateless probe ran %d times for an snmp check", n)
	}
	if b := h.rec.Batches(); len(b) != 1 || b[0].obs[0].Metric != "snmp.up" || b[0].obs[0].Err != "" {
		t.Fatalf("recorded %+v, want the poll's result", b)
	}
}

// TestSecretReachesAStatelessProbe: a plugin without state gets its secret
// through the probe function, params untouched.
func TestSecretReachesAStatelessProbe(t *testing.T) {
	h := newHarness(t, nil)
	h.r.describe = vaultDescriptor
	h.secrets.set(h.org, "v", map[string]string{"token": "t0ken"})
	check := checks.Resolved{Key: "vault", Plugin: "vault", Interval: time.Minute, Params: map[string]any{"key": "vault", "secret": "v"}}
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true}, check)
	h.tickAt(0)
	c := h.probe.Calls()
	if len(c) != 1 || c[0].secret["token"] != "t0ken" || len(c[0].secret) != 1 {
		t.Fatalf("calls = %+v, want one with the secret's fields", c)
	}
	if !reflect.DeepEqual(c[0].params, map[string]any{"key": "vault", "secret": "v"}) {
		t.Fatalf("params = %v, want them unchanged", c[0].params)
	}
}

// TestNoSecretNoLookup: a plugin that takes no secret is probed with a
// nil one and never reaches the store.
func TestNoSecretNoLookup(t *testing.T) {
	h := newHarness(t, nil)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		synthetic("web", "http", time.Minute))
	h.tickAt(0)
	if c := h.probe.Calls(); len(c) != 1 || c[0].secret != nil {
		t.Fatalf("calls = %+v, want one without a secret", c)
	}
	if l := h.secrets.Lookups(); len(l) != 0 {
		t.Fatalf("looked up %+v for a plugin without a secret", l)
	}
}

// failedSecretCase is a secret that cannot be handed over, the error
// every metric carries, and whether the runner logs it. Only a store
// failure is logged: a missing secret, or no store, is a configuration
// state that recurs on every poll and is shown on the host page instead.
type failedSecretCase struct {
	edit    func(*harness, *Deps)
	wantErr string
	logged  bool
}

func failedSecretCases() map[string]failedSecretCase {
	return map[string]failedSecretCase{
		"missing": {wantErr: `secret "core-sw" is not defined`},
		"store error": {
			edit: func(h *harness, _ *Deps) {
				h.secrets.err = errors.New("dial tcp 10.9.8.7:5432: password authentication failed for user pulsix")
			},
			wantErr: errSecretStore,
			logged:  true,
		},
		"no store": {
			edit:    func(_ *harness, d *Deps) { d.Secrets = nil },
			wantErr: errNoSecretStore,
		},
	}
}

// TestUnavailableSecretFailsEveryMetric: a secret that is missing, a store
// that fails and no store at all are each an error on every metric the
// check would have reported, and the device is never polled.
func TestUnavailableSecretFailsEveryMetric(t *testing.T) {
	for name, tc := range failedSecretCases() {
		t.Run(name, func(t *testing.T) {
			var logs syncBuffer
			h := newHarness(t, func(h *harness, d *Deps) {
				d.Log = slog.New(slog.NewTextHandler(&logs, nil))
				if tc.edit != nil {
					tc.edit(h, d)
				}
			})
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
				snmpCheck("sw", "core-sw", time.Minute))
			h.tickAt(0)
			if warned := strings.Contains(logs.String(), "level=WARN"); warned != tc.logged {
				t.Fatalf("warned = %v, want %v; log %q", warned, tc.logged, logs.String())
			}
			for _, p := range h.pollers.Built() {
				if n := len(p.Calls()); n != 0 {
					t.Fatalf("polled %d times without a secret", n)
				}
			}
			b := h.rec.Batches()
			if len(b) != 1 {
				t.Fatalf("batches = %+v, want one", b)
			}
			type mi struct{ metric, item string }
			want := []mi{{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""}, {"snmp.value", "name"}}
			if len(b[0].obs) != len(want) {
				t.Fatalf("observations = %+v, want %v", b[0].obs, want)
			}
			for i, o := range b[0].obs {
				if o.Metric != want[i].metric || o.Item != want[i].item || o.Err != tc.wantErr || o.Value != nil ||
					o.HostID != h.host || o.CheckKey != "sw" {
					t.Fatalf("observation %d = %+v, want error %q on %v", i, o, tc.wantErr, want[i])
				}
			}
		})
	}
}

// TestUnavailableSecretOfAStatelessPlugin: the error is on each of the
// descriptor's metrics, and the probe is not called.
func TestUnavailableSecretOfAStatelessPlugin(t *testing.T) {
	h := newHarness(t, nil)
	h.r.describe = vaultDescriptor
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		checks.Resolved{Key: "vault", Plugin: "vault", Interval: time.Minute, Params: map[string]any{"key": "vault", "secret": "v"}})
	h.tickAt(0)
	if n := len(h.probe.Calls()); n != 0 {
		t.Fatalf("probed %d times without the secret", n)
	}
	b := h.rec.Batches()
	if len(b) != 1 || len(b[0].obs) != 2 ||
		b[0].obs[0].Metric != "vault.up" || b[0].obs[1].Metric != "vault.sealed" ||
		b[0].obs[0].Err != `secret "v" is not defined` || b[0].obs[1].Err != `secret "v" is not defined` {
		t.Fatalf("batches = %+v, want the error on vault.up and vault.sealed", b)
	}
}

// TestSecretStoreErrorIsLoggedNotRecorded: the store's own error text may
// carry driver details, so it is logged (warn, with org, host and check)
// and the observations carry a fixed text.
func TestSecretStoreErrorIsLoggedNotRecorded(t *testing.T) {
	var logs syncBuffer
	h := newHarness(t, func(h *harness, d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, nil))
		h.secrets.err = errors.New("dial tcp 10.9.8.7:5432: refused")
	})
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		snmpCheck("sw", "core-sw", time.Minute))
	h.tickAt(0)
	for _, b := range h.rec.Batches() {
		for _, o := range b.obs {
			if strings.Contains(o.Err, "10.9.8.7") {
				t.Fatalf("observation %+v carries the store's error", o)
			}
		}
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", "10.9.8.7", "org=" + h.org.String(), "host=" + h.host.String(), "check=sw"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log %q lacks %q", out, want)
		}
	}
}

// TestSecretValuesNeverLeak: no field value reaches the recorded
// observations or the log, whatever the poll reports.
func TestSecretValuesNeverLeak(t *testing.T) {
	var logs syncBuffer
	h := newHarness(t, func(h *harness, d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		h.pollers.skipped = true
	})
	h.secrets.set(h.org, "core-sw", swFields)
	h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
		snmpCheck("sw", "core-sw", time.Minute))
	h.tickAt(0)
	if out := logs.String(); out == "" || strings.Contains(out, "s3cret-community") {
		t.Fatalf("log %q: want the skip logged and no secret value", out)
	}
	for _, b := range h.rec.Batches() {
		for _, o := range b.obs {
			if strings.Contains(o.Err, "s3cret") || (o.Label != nil && strings.Contains(*o.Label, "s3cret")) {
				t.Fatalf("observation %+v carries a secret value", o)
			}
		}
	}
}

// TestSecretStoreErrorAtShutdownIsNotLogged: a lookup cut short by the
// runner's own cancellation measured nothing worth a warning; one that
// ran into the probe's deadline is a real failure and is logged. Both
// report the fixed text.
func TestSecretStoreErrorAtShutdownIsNotLogged(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx    func() (context.Context, context.CancelFunc)
		logged bool
	}{
		"cancelled": {
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
		},
		"deadline": {
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			logged: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var logs syncBuffer
			h := newHarness(t, func(h *harness, d *Deps) {
				d.Log = slog.New(slog.NewTextHandler(&logs, nil))
				h.secrets.err = errors.New("dial tcp 10.9.8.7:5432: refused")
			})
			s := slot{host: h.host, key: "sw"}
			tg := target{org: h.org, address: "192.0.2.1", check: snmpCheck("sw", "core-sw", time.Minute), secret: "core-sw"}
			ctx, cancel := tc.ctx()
			defer cancel()
			obs := h.r.runProbe(ctx, s, tg, h.pollers.New())
			if len(obs) == 0 || obs[0].Err != errSecretStore {
				t.Fatalf("observations = %+v, want the fixed store error", obs)
			}
			if n := len(h.pollers.Built()[0].Calls()); n != 0 {
				t.Fatalf("polled %d times without the secret", n)
			}
			if warned := strings.Contains(logs.String(), "level=WARN"); warned != tc.logged {
				t.Fatalf("warned = %v, want %v; log %q", warned, tc.logged, logs.String())
			}
		})
	}
}

// TestUnavailableSecretOfAnHTTPCheck: an http check's missing secret is
// an error on the metrics its params produce, the JSON values on their
// items, and on nothing else: no item-less http.json.* or http.body_match
// row appears for a check that does not ask for them.
func TestUnavailableSecretOfAnHTTPCheck(t *testing.T) {
	type mi struct{ metric, item string }
	for name, tc := range map[string]struct {
		params map[string]any
		want   []mi
	}{
		"json values": {
			params: map[string]any{"url": "https://api.example.com/health", "json_values": []any{"status=$.status", "depth=$.queue.depth"}},
			want: []mi{{"http.up", ""}, {"http.status_code", ""}, {"http.response_ms", ""},
				{"http.json.value", "status"}, {"http.json.value", "depth"}},
		},
		"plain": {
			params: map[string]any{"url": "https://api.example.com/health"},
			want:   []mi{{"http.up", ""}, {"http.status_code", ""}, {"http.response_ms", ""}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			params := map[string]any{"key": "api", "secret": "api-token"}
			maps.Copy(params, tc.params)
			h.world.addHost(h.org, hosts.Host{ID: h.host, Address: "192.0.2.1", Monitored: true},
				checks.Resolved{Key: "api", Plugin: "http", Interval: time.Minute, Params: params})
			h.tickAt(0)
			if n := len(h.probe.Calls()); n != 0 {
				t.Fatalf("probed %d times without the secret", n)
			}
			b := h.rec.Batches()
			if len(b) != 1 || len(b[0].obs) != len(tc.want) {
				t.Fatalf("batches = %+v, want errors on %v", b, tc.want)
			}
			for i, o := range b[0].obs {
				if o.Metric != tc.want[i].metric || o.Item != tc.want[i].item || o.Err != `secret "api-token" is not defined` {
					t.Fatalf("observation %d = %+v, want the error on %v", i, o, tc.want[i])
				}
			}
		})
	}
}
