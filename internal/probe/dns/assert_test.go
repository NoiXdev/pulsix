package dns_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/judge"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func assertions(entries ...string) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return out
}

// verdicts are the assertion items' observations: item → "result actual",
// or "!error".
func verdicts(obs []probe.Observation) map[string]string {
	out := map[string]string{}
	for _, o := range obs {
		if o.Item == "" {
			continue
		}
		switch {
		case o.Err != "":
			out[o.Item+" "+o.Metric] = "!" + o.Err
		case o.Label != nil:
			out[o.Item+" "+o.Metric] = *o.Label
		}
	}
	return out
}

// TestDNSAssertions (backlog 3.87): the answers as one sorted text, their
// count, one answer by expect's comparison and the lookup time.
func TestDNSAssertions(t *testing.T) {
	server := startDNSStub(t, map[string][]netip.Addr{"example.test": {
		netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("192.0.2.10"),
	}})
	obs := run(context.Background(), probetest.LoopbackGuard(), dnsParams(server, map[string]any{
		"name": "example.test",
		"assertions": assertions(
			"t=resolve_ms < 5000",
			"n=answer_count == 2",
			`all=answers == "192.0.2.10, 192.0.2.9"`,
			`has=answer("192.0.2.9") exists`,
			`not=answer("203.0.113.1") !exists`,
			`miss=answer("203.0.113.1") exists @warn`,
			`gone=answer("192.0.2.10") !exists`,
			"bad=status == 200",
		),
	}), "")
	probetest.WantUp(t, obs[:3], "dns")
	got := verdicts(obs)
	want := map[string]string{
		"t dns.assertion": "pass",
		"n dns.assertion": "pass", "n dns.assertion_actual": "2",
		"all dns.assertion": "pass", "all dns.assertion_actual": `"192.0.2.10, 192.0.2.9"`,
		"has dns.assertion": "pass", "has dns.assertion_actual": `"192.0.2.9"`,
		"not dns.assertion": "pass", "not dns.assertion_actual": judge.ActualAbsent,
		"miss dns.assertion": "warn", "miss dns.assertion_actual": judge.ActualAbsent,
		"gone dns.assertion": "crit", "gone dns.assertion_actual": `"192.0.2.10"`,
		"bad dns.assertion": "!" + judge.ErrInvalid, "bad dns.assertion_actual": "!" + judge.ErrInvalid,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 16 {
		t.Errorf("assertion observations = %v, want 16", got)
	}
}

// TestDNSAssertionsOnFailures: a down lookup reports no assertion item;
// one that could not be made puts its error on both metrics of each.
func TestDNSAssertionsOnFailures(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	list := assertions("n=answer_count > 0")
	obs := run(context.Background(), probetest.LoopbackGuard(), dnsParams(server, map[string]any{"name": "missing.test", "assertions": list}), "")
	probetest.WantDown(t, obs, "dns", probe.FailureNXDomain)
	if got := verdicts(obs); len(got) != 0 {
		t.Errorf("down lookup reported %v", got)
	}
	obs = run(context.Background(), probe.NewGuard(nil, nil), dnsParams(server, map[string]any{"name": "example.test", "assertions": list}), "")
	got := verdicts(obs)
	if got["n dns.assertion"] != "!target address not allowed" || got["n dns.assertion_actual"] != "!target address not allowed" {
		t.Errorf("refused lookup = %v", got)
	}
}
