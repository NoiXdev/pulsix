// Job chaining end to end (backlog 2.121): a real manager, worker and the
// fake ansible-runner. Template A follows up with template B; the chained
// run starts on the hosts A played on, a failed run skips its follow-up and
// a follow-up cycle is refused.
package e2e

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/app"
)

const chainWait = 60 * time.Second

func TestChainEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	if os.Getenv("PULSIX_E2E_REAL_ANSIBLE") == "1" {
		t.Skip("the chain scenario drives the fake runner")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tick := 200 * time.Millisecond
	s := wireScenario(t, ctx, cancel, wiring{opts: []app.Option{app.WithChainTick(tick)}})
	s.pinHostKey()

	h2 := s.createManagedHost("e2e-chain-h2")
	bPB := s.createEditorPlaybook("chain-b", greetTemplate(t))
	b := s.createTemplateScoped("chain-b", bPB, []string{s.hostID, h2}, nil)
	aPB := s.createEditorPlaybook("chain-a", failTemplate(t))
	a := s.createTemplateScoped("chain-a", aPB, []string{s.hostID}, []string{b})
	s.setTemplateVar(a, aPB, "false")

	// --- The chain: A succeeds, B starts on A's hosts only ----------------
	aJob := s.runTemplate(a)
	aDone := s.awaitJob(aJob, "chain parent", chainWait)
	if aDone.Job.Status != "succeeded" {
		t.Fatalf("A = %q (error %q), want succeeded%s", aDone.Job.Status, aDone.Job.Error, s.runnerOutput())
	}
	child := s.awaitChainJob(chainWait)
	childDone := s.awaitJob(child.ID, "chained run", chainWait)
	if childDone.Job.Status != "succeeded" {
		t.Fatalf("chained run = %q (error %q), want succeeded%s", childDone.Job.Status, childDone.Job.Error, s.runnerOutput())
	}
	if child.TriggerRef != aDone.Job.RunGroupID {
		t.Fatalf("chained trigger_ref = %q, want A's run group %q", child.TriggerRef, aDone.Job.RunGroupID)
	}
	if !slices.Equal(childDone.Job.Hosts, []string{s.hostID}) {
		t.Fatalf("chained run hosts = %v, want only %s (h2 %s left out)", childDone.Job.Hosts, s.hostID, h2)
	}
	aDetail := s.chainOf(aJob)
	if len(aDetail.FollowUps) != 1 {
		t.Fatalf("A chain follow_ups = %+v, want 1", aDetail.FollowUps)
	}
	fu := aDetail.FollowUps[0]
	if fu.State != "started" || fu.ChildFirstJobID != child.ID || !slices.Equal(fu.Hosts, []string{s.hostID}) {
		t.Fatalf("A follow-up = %+v, want started child %s on [%s]", fu, child.ID, s.hostID)
	}
	if p := s.chainOf(child.ID).Parent; p == nil || p.TemplateName != "chain-a" || p.FirstJobID != aJob {
		t.Fatalf("chained run parent = %+v, want template A first job %s", p, aJob)
	}

	// --- A failed run skips its follow-up ---------------------------------
	s.setTemplateVar(a, aPB, "true")
	failJob := s.runTemplate(a)
	failed := s.awaitJob(failJob, "failing parent", chainWait)
	if failed.Job.Status != "failed" {
		t.Fatalf("A with the failing var = %q, want failed%s", failed.Job.Status, s.runnerOutput())
	}
	deadline := time.Now().Add(chainWait)
	var skipped bool
	for !skipped {
		for _, f := range s.chainOf(failJob).FollowUps {
			if f.State == "skipped" && f.Reason == "the run failed" {
				skipped = true
			}
		}
		if !skipped {
			if time.Now().After(deadline) {
				t.Fatalf("follow-up of the failed run not skipped: %+v", s.chainOf(failJob).FollowUps)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	time.Sleep(2*tick + 300*time.Millisecond)
	if n := len(s.chainJobs()); n != 1 {
		t.Fatalf("chain jobs after the failed run = %d, want still 1", n)
	}

	// --- A follow-up cycle is refused -------------------------------------
	status, body := s.updateFollowUps(b, "chain-b", bPB, []string{a})
	if status != 422 || !strings.Contains(body, "follow-ups would form a cycle: chain-b → chain-a → chain-b") {
		t.Fatalf("cycle update = %d %s, want 422 naming chain-b → chain-a → chain-b", status, body)
	}
}

// chainJob is a job list row of a chained run.
type chainJob struct {
	ID         string `json:"id"`
	TriggerRef string `json:"trigger_ref"`
}

// chainJobs lists the runs the follow-up starter started.
func (s *scenario) chainJobs() []chainJob {
	s.t.Helper()
	var out struct {
		Jobs []chainJob `json:"jobs"`
	}
	s.get("/api/v1/"+s.apiPath()+"/jobs?trigger=chain", &out)
	return out.Jobs
}

// awaitChainJob polls until one chained run exists.
func (s *scenario) awaitChainJob(timeout time.Duration) chainJob {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if jobs := s.chainJobs(); len(jobs) > 0 {
			return jobs[0]
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("no chained run started in %s", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// setTemplateVar flips the failing playbook's should_fail var on template id.
func (s *scenario) setTemplateVar(id, playbookID, value string) {
	s.t.Helper()
	s.put("/api/v1/"+s.apiPath()+"/job-templates/"+id, map[string]any{
		"name": "chain-a", "playbook_id": playbookID,
		"scope":      map[string]any{"host_ids": []string{s.hostID}},
		"extra_vars": map[string]any{"should_fail": value},
	}, &templateOut{})
}

type chainView struct {
	Parent *struct {
		GroupID      string `json:"group_id"`
		FirstJobID   string `json:"first_job_id"`
		TemplateName string `json:"template_name"`
	} `json:"parent"`
	FollowUps []struct {
		State           string   `json:"state"`
		Reason          string   `json:"reason"`
		ChildFirstJobID string   `json:"child_first_job_id"`
		Hosts           []string `json:"hosts"`
	} `json:"follow_ups"`
}

// chainOf reads a run's chain block.
func (s *scenario) chainOf(jobID string) chainView {
	s.t.Helper()
	var out struct {
		Job struct {
			Chain chainView `json:"chain"`
		} `json:"job"`
	}
	s.get("/api/v1/"+s.apiPath()+"/jobs/"+jobID, &out)
	return out.Job.Chain
}
