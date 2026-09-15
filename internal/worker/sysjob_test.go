// The system-playbook executors' shared test harness: the claim helpers
// and the delegating-executor fakes (the fake runner binary lives in
// system_e2e_test.go, where the real delegation runs).
package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// sysJob builds the claimed job the manager ships for a system playbook
// kind, with the kind payload the tests assemble themselves.
func sysJob(t *testing.T, kind, payload string, hostKeys map[string]string) *workerpb.Job {
	t.Helper()
	return &workerpb.Job{
		Id: "01900000-0000-7000-8000-000000000abc", Kind: kind,
		OrgId: "01900000-0000-7000-8000-000000000eee", ZoneId: "01900000-0000-7000-8000-000000000fff",
		HostKeys: hostKeys, KindPayload: payload,
	}
}

// sysPayload marshals a kind payload map.
func sysPayload(t *testing.T, m map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// collector records every event the executor forwards.
type evCollector struct {
	events []*workerpb.RunEvent
	err    error
}

func (c *evCollector) sink(ev *workerpb.RunEvent) error {
	if c.err != nil {
		return c.err
	}
	c.events = append(c.events, ev)
	return nil
}

// captureExecutor is the fake delegate the executor tests shape claims
// against: it records what reached the runner without running anything.
type captureExecutor struct {
	got    *workerpb.Job
	status Status
	stats  map[string]any
	err    error
}

func (f *captureExecutor) Run(_ context.Context, job *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	f.got = job
	return f.status, f.stats, f.err
}
