package snmp_test

import (
	"context"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
	"github.com/noixdev/pulsix/internal/probe/snmp"
)

// TestFailAll: a poll that could not be attempted has msg on every metric
// the params would have produced: the host metrics and each oids entry's
// value, an unusable OID's included.
func TestFailAll(t *testing.T) {
	type want struct{ metric, item string }
	for name, tc := range map[string]struct {
		params map[string]any
		want   []want
	}{
		"entries": {
			params: map[string]any{"secret": "sw", "oids": []any{"name=1.3.6.1.2.1.1.5.0", "bad=3.6.1.2"}, "interfaces": true},
			want: []want{
				{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""},
				{"snmp.value", "name"}, {"snmp.value", "bad"},
			},
		},
		"no entries": {
			params: map[string]any{"secret": "sw"},
			want:   []want{{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""}},
		},
		"unreadable params": {
			params: map[string]any{"secret": "sw", "version": "v1", "oids": []any{"name=1.3.6.1.2.1.1.5.0"}},
			want:   []want{{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			obs := snmp.FailAll(tc.params, `secret "sw" is not defined`)
			if len(obs) != len(tc.want) {
				t.Fatalf("got %+v, want %d observations", obs, len(tc.want))
			}
			for i, o := range obs {
				if o.Metric != tc.want[i].metric || o.Item != tc.want[i].item ||
					o.Err != `secret "sw" is not defined` || o.Value != nil || o.Label != nil {
					t.Fatalf("observation %d = %+v, want an error on %+v", i, o, tc.want[i])
				}
			}
		})
	}
}

// TestFailAllMatchesAnUnusableSecret: the shape is the one Poll gives a
// secret it cannot use, so a missing secret and a missing field read the
// same on the host page.
func TestFailAllMatchesAnUnusableSecret(t *testing.T) {
	params := map[string]any{"secret": "sw", "oids": []any{"a=1.3.6.1.2.1.1.5.0", "b=1.3.6.1.2.1.1.6.0"}}
	polled, _ := snmp.NewPoller(nil).Poll(context.Background(), probetest.LoopbackGuard(), params, "127.0.0.1", nil)
	failed := snmp.FailAll(params, "x")
	if len(polled) != len(failed) {
		t.Fatalf("Poll without a secret gave %+v, FailAll %+v", polled, failed)
	}
	for i := range polled {
		if polled[i].Metric != failed[i].Metric || polled[i].Item != failed[i].Item || polled[i].Err == "" {
			t.Fatalf("observation %d: Poll %+v, FailAll %+v", i, polled[i], failed[i])
		}
	}
}
