// The playbook dry run (backlog 3.126) through the whole stack: the dry-run
// route enqueues a check-mode job, the worker announces check mode support
// and claims it, and ansible-runner runs the play with --check (real mode)
// or the fake honours the same --cmdline (fake mode). A task that would
// create a file leaves none behind, the recap still counts the change per
// host, and a real run of the same template afterwards does create it — so
// the missing file is the dry run's doing, not the playbook's.
package e2e

import (
	"os"
	"path/filepath"
	"time"
)

func (s *scenario) dryRun() {
	s.t.Helper()
	marker := filepath.Join(s.t.TempDir(), "pulsix-e2e-dry-run")
	pb := s.createEditorPlaybook("dry-run", fixtureFile(s.t, "playbooks/dryrun.yml"))
	tid := s.createTemplate("dry-run", pb, map[string]any{"dry_marker": marker})

	var out jobStartOut
	s.post("/api/v1/"+s.apiPath()+"/playbooks/"+pb+"/dry-run", map[string]any{"template_id": tid}, &out)
	if len(out.Jobs) != 1 {
		s.t.Fatalf("dry run created %d job(s), want 1", len(out.Jobs))
	}
	dry := s.awaitJob(out.Jobs[0].ID, "run", 90*time.Second)
	if dry.Job.Status != "succeeded" {
		s.t.Fatalf("dry run = %q (error %q)%s", dry.Job.Status, dry.Job.Error, s.runnerOutput())
	}
	if !dry.Job.CheckMode {
		s.t.Fatal("the dry run's job is not marked check mode")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		s.t.Fatalf("the dry run left the marker file behind (stat: %v)%s", err, s.runnerOutput())
	}
	if got := s.hostCount(dry, "changed"); got != 1 {
		s.t.Fatalf("dry run host_counts[%s].changed = %v, want 1 (stats %v)", s.hostID, got, dry.Job.Stats)
	}

	// The same template run for real creates the file.
	run := s.awaitJob(s.runTemplate(tid), "run", 90*time.Second)
	if run.Job.Status != "succeeded" || run.Job.CheckMode {
		s.t.Fatalf("real run = %q check mode %v (error %q)%s", run.Job.Status, run.Job.CheckMode, run.Job.Error, s.runnerOutput())
	}
	if _, err := os.Stat(marker); err != nil {
		s.t.Fatalf("the real run did not create the marker file: %v%s", err, s.runnerOutput())
	}
}

// hostCount reads one of the scenario host's recap counts from a run's
// stats (host_counts, keyed by host id).
func (s *scenario) hostCount(run jobDetailOut, name string) any {
	s.t.Helper()
	counts, _ := run.Job.Stats["host_counts"].(map[string]any)
	host, _ := counts[s.hostID].(map[string]any)
	if n, ok := host[name].(float64); ok {
		return int(n)
	}
	return host[name]
}
