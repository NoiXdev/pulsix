package agents_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agentapi"
	"github.com/noixdev/pulsix/internal/agents"
)

// The facts service half (Task 8): bounds are REJECTED with a sentinel
// (never silently clipped — the agent-side collector already normalizes,
// so a violation here is a lying agent, not bad luck), everything else is
// forwarded to the store in one call.

var (
	testOrg  = uuid.Must(uuid.NewV7())
	testHost = uuid.Must(uuid.NewV7())
)

func newFactsService() (*agents.Service, *fakeStore) {
	f := &fakeStore{}
	return agents.NewService(f), f
}

// TestRecordFactsValidatesBounds: over the key budget or over the value
// bound is agents.ErrInvalidInput — mapped to InvalidArgument by the gRPC
// transport — and nothing reaches the store.
func TestRecordFactsValidatesBounds(t *testing.T) {
	cases := []struct {
		name  string
		facts map[string]string
		want  string
	}{
		{
			name: "too many keys",
			facts: func() map[string]string {
				m := map[string]string{}
				for i := 0; i < agents.MaxFactsKeys+1; i++ {
					m[strings.Repeat("k", 1)+strings.Repeat("_", i)] = "v"
				}
				return m
			}(),
			want: "at most",
		},
		{
			name: "over-long value",
			facts: map[string]string{
				"os": strings.Repeat("x", agents.MaxFactsValueLen+1),
			},
			want: "exceeds",
		},
		{
			name:  "empty key",
			facts: map[string]string{"": "v"},
			want:  "empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, f := newFactsService()
			err := svc.RecordFacts(ctx, testOrg, testHost, tc.facts)
			if !errors.Is(err, agents.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
			if calls := f.factCalls(); len(calls) != 0 {
				t.Fatalf("rejected facts reached the store: %+v", calls)
			}
		})
	}
}

// TestRecordFactsAcceptsBoundaryShapes: exactly at the limits is legal,
// and an empty push is a silent no-op.
func TestRecordFactsAcceptsBoundaryShapes(t *testing.T) {
	atLimit := strings.Repeat("x", agents.MaxFactsValueLen) // exactly 512
	full := map[string]string{"os": atLimit, "arch": "amd64", "virt": "kvm"}
	svc, f := newFactsService()
	if err := svc.RecordFacts(ctx, testOrg, testHost, full); err != nil {
		t.Fatalf("at-limit facts: %v", err)
	}
	if calls := f.factCalls(); len(calls) != 1 || calls[0].Facts["os"] != atLimit {
		t.Fatalf("forwarded facts = %+v", calls)
	}
	// An empty push is a no-op, not an error (nothing to merge, nothing to
	// tag) — but it still consults the store: an unknown host stays
	// ErrNotFound even for an empty map? No — the service short-circuits
	// an empty map BEFORE the store: there is no fact work a store could
	// do on an empty set.
	svc2, f2 := newFactsService()
	if err := svc2.RecordFacts(ctx, testOrg, testHost, nil); err != nil {
		t.Fatalf("nil facts: %v", err)
	}
	if calls := f2.factCalls(); len(calls) != 0 {
		t.Fatalf("empty push reached the store: %+v", calls)
	}
}

// TestRecordFactsForwardsToStore: the service is a pass-through for valid
// facts (org, host, map verbatim).
func TestRecordFactsForwardsToStore(t *testing.T) {
	svc, f := newFactsService()
	facts := map[string]string{"os": "linux", "ips": "10.0.0.1,192.168.0.2"}
	if err := svc.RecordFacts(ctx, testOrg, testHost, facts); err != nil {
		t.Fatalf("record: %v", err)
	}
	calls := f.factCalls()
	if len(calls) != 1 || calls[0].OrgID != testOrg || calls[0].HostID != testHost ||
		calls[0].Facts["os"] != "linux" || calls[0].Facts["ips"] != "10.0.0.1,192.168.0.2" {
		t.Fatalf("forwarded = %+v", calls)
	}
}

// TestServiceSatisfiesFactsSink: the port agentapi wires (Deps.Facts) is
// satisfied by the service itself — compile-time, no import cycle (the
// interface lives over in agentapi; agents never imports it).
var _ agentapi.FactsSink = (*agents.Service)(nil)

// TestRecordFactsStoreFaultPropagates: a store fault passes through for
// the transport to map (Internal), not swallowed.
func TestRecordFactsStoreFaultPropagates(t *testing.T) {
	svc, f := newFactsService()
	boom := errors.New("db down")
	f.factsErr = boom
	if err := svc.RecordFacts(ctx, testOrg, testHost, map[string]string{"os": "linux"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store fault", err)
	}
}
