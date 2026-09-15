// The kind dispatch: claimed jobs route to the executor their kind
// belongs to, and a kind with no executor in this build is an error —
// never a silent skip.
package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// stubExec records the job it ran, so a composite's routing is observable.
type stubExec struct {
	ran []string
}

func (s *stubExec) Run(_ context.Context, job *workerpb.Job, _ RunEnv, _ func(*workerpb.RunEvent) error) (Status, map[string]any, error) {
	s.ran = append(s.ran, job.GetKind())
	return StatusSucceeded, map[string]any{"kind": job.GetKind()}, nil
}

func TestCompositeRoutesByKind(t *testing.T) {
	runs, adhocs := &stubExec{}, &stubExec{}
	c := NewCompositeExecutor(map[string]Executor{
		"run":   runs,
		"adhoc": adhocs,
	})
	env := RunEnv{WorkDir: t.TempDir()}

	status, stats, err := c.Run(t.Context(), &workerpb.Job{Kind: "run"}, env, func(*workerpb.RunEvent) error { return nil })
	if err != nil || status != StatusSucceeded || stats["kind"] != "run" {
		t.Fatalf("run job: status=%q stats=%v err=%v", status, stats, err)
	}
	status, _, err = c.Run(t.Context(), &workerpb.Job{Kind: "adhoc"}, env, func(*workerpb.RunEvent) error { return nil })
	if err != nil || status != StatusSucceeded {
		t.Fatalf("adhoc job: status=%q err=%v", status, err)
	}
	if len(runs.ran) != 1 || runs.ran[0] != "run" || len(adhocs.ran) != 1 || adhocs.ran[0] != "adhoc" {
		t.Fatalf("routing = %v / %v", runs.ran, adhocs.ran)
	}
}

func TestCompositeUnknownKindIsAnError(t *testing.T) {
	c := NewCompositeExecutor(map[string]Executor{"run": &stubExec{}})
	status, stats, err := c.Run(t.Context(), &workerpb.Job{Kind: "bootstrap"}, RunEnv{}, func(*workerpb.RunEvent) error { return nil })
	if err == nil {
		t.Fatal("a kind with no executor must fail")
	}
	if status != StatusError || stats != nil {
		t.Errorf("status = %q stats = %v, want error/nil", status, stats)
	}
	if !strings.Contains(err.Error(), "bootstrap") {
		t.Errorf("the error names the kind: %v", err)
	}
}
