package docker

import (
	"context"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// encodesAsCheckResult fails the test when o could not travel as the
// CheckResult it is sent as.
func encodesAsCheckResult(t *testing.T, o checks.Observation) {
	t.Helper()
	r := &agentpb.CheckResult{CheckKey: "docker", Item: o.Item, Metric: o.Metric}
	switch {
	case o.Value != nil:
		r.Observation = &agentpb.CheckResult_Value{Value: *o.Value}
	case o.Label != nil:
		r.Observation = &agentpb.CheckResult_Label{Label: *o.Label}
	default:
		r.Observation = &agentpb.CheckResult_Error{Error: o.Err}
	}
	if _, err := proto.Marshal(r); err != nil {
		t.Fatalf("observation %q/%q does not encode: %v", o.Item, o.Metric, err)
	}
}

// TestDockerSkipsContainersTheManagerWouldRefuse is backlog 7.40 for the
// docker check. The manager refuses a whole check-results batch, and ends
// the host's stream, when one item breaks its contract, and a container
// name is whatever whoever can start a container chose. A container the
// agent cannot name inside the contract is left out, named in the plugin's
// advisory error, and not counted, so it costs every other container
// nothing.
func TestDockerSkipsContainersTheManagerWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		desc string
		name string
	}{
		{"longer than the item bound", strings.Repeat("n", 600)},
		{"carrying a NUL byte", "we\x00b"},
		{"not valid UTF-8", "q\xffz"},
		{"carrying a line break", "a\nb"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := &fakeDockerEngine{
				containers: []containerSummary{
					{ID: "a", Name: "web", State: "running"},
					{ID: "b", Name: tc.name, State: "running"},
				},
				details: map[string]containerDetail{"a": {Health: "none"}, "b": {Health: "none"}},
			}
			obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "cannot be reported") {
				t.Fatalf("Collect error = %v, want one naming the skipped container", err)
			}
			if !utf8.ValidString(err.Error()) || strings.ContainsRune(err.Error(), 0) {
				t.Fatalf("the error quotes the name raw: %q", err.Error())
			}
			if got := metricItems(obs, "docker.state"); !slices.Equal(got, []string{"web"}) {
				t.Fatalf("reported containers = %q, want only web", got)
			}
			if v := findObs(t, obs, "", "docker.containers_total").Value; v == nil || *v != 1 {
				t.Errorf("total = %v, want 1", v)
			}
			if v := findObs(t, obs, "", "docker.containers_running").Value; v == nil || *v != 1 {
				t.Errorf("running = %v, want 1", v)
			}
			if v := findObs(t, obs, "", "docker.containers_unreported").Value; v == nil || *v != 1 {
				t.Errorf("unreported = %v, want 1", v)
			}
			for _, o := range obs {
				encodesAsCheckResult(t, o)
			}
		})
	}
}

// TestDockerRefusesLabelsTheManagerWouldRefuse: the state comes from the
// list and the health from the inspect, both engine-supplied strings. One
// the manager would refuse (over its label bound, a NUL byte) or gRPC could
// not encode (invalid UTF-8) becomes an Err observation for that metric;
// the container's other metrics are still reported.
func TestDockerRefusesLabelsTheManagerWouldRefuse(t *testing.T) {
	long := strings.Repeat("s", 600)
	for _, tc := range []struct {
		desc, state, health string
		badMetric           string
	}{
		{"over-long state", long, "healthy", "docker.state"},
		{"state with NUL", "run\x00ning", "healthy", "docker.state"},
		{"state not valid UTF-8", "run\xffning", "healthy", "docker.state"},
		{"over-long health", "running", long, "docker.health"},
		{"health with NUL", "running", "heal\x00thy", "docker.health"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			f := &fakeDockerEngine{
				containers: []containerSummary{{ID: "a", Name: "web", State: tc.state}},
				details:    map[string]containerDetail{"a": {Health: tc.health, RestartCount: 2}},
			}
			obs, err := newTestDocker(f, nil).Collect(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			bad := findObs(t, obs, "web", tc.badMetric)
			if bad.Label != nil || bad.Err == "" {
				t.Errorf("%s = %+v, want an Err observation", tc.badMetric, bad)
			}
			for _, m := range []string{"docker.state", "docker.health"} {
				if m == tc.badMetric {
					continue
				}
				if o := findObs(t, obs, "web", m); o.Label == nil {
					t.Errorf("%s = %+v, want a label", m, o)
				}
			}
			if v := findObs(t, obs, "web", "docker.restart_count").Value; v == nil || *v != 2 {
				t.Errorf("restart_count = %v", v)
			}
			for _, o := range obs {
				encodesAsCheckResult(t, o)
			}
		})
	}
}
