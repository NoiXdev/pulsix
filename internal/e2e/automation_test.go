// The v0.2c milestone gate: the same fully wired manager as the v0.2b
// gate, but the worker enrolls with a one-time token over the API, a
// schedule fires on an injected clock, a signed webhook starts a run, and a
// revocation cuts the worker off.
package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/worker"
)

// tokenWorkerName is the name the token-enrolled worker is registered as.
const tokenWorkerName = "e2e-token-worker"

// fakeClock is the schedule ticker's clock: real time until the test moves
// it, so everything else in the manager keeps its wall-clock behaviour.
type fakeClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

// setAt moves the clock so that Now() reads at (and keeps running from there).
func (c *fakeClock) setAt(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset = time.Until(at)
}

type (
	workersOut struct {
		Workers []struct {
			ID            string     `json:"id"`
			Name          string     `json:"name"`
			Status        string     `json:"status"`
			CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
		} `json:"workers"`
	}
	scheduleRunOut struct {
		Outcome    string `json:"outcome"`
		Reason     string `json:"reason,omitempty"`
		JobGroupID string `json:"job_group_id,omitempty"`
	}
	scheduleOut struct {
		Schedule struct {
			ID        string     `json:"id"`
			NextRunAt *time.Time `json:"next_run_at,omitempty"`
		} `json:"schedule"`
	}
	jobListOut struct {
		Jobs []struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			Trigger    string `json:"trigger"`
			TriggerRef string `json:"trigger_ref,omitempty"`
		} `json:"jobs"`
	}
	triggerCreatedOut struct {
		Trigger struct {
			ID string `json:"id"`
		} `json:"trigger"`
		Secret string `json:"secret"`
		URL    string `json:"url"`
	}
	hookOut struct {
		RunID string `json:"run_id"`
	}
)

func TestAutomationEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("no docker for the testcontainer database: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clock := &fakeClock{}
	s := wireScenario(t, ctx, cancel, wiring{
		tokenWorker:  true,
		scheduleTick: 100 * time.Millisecond,
		opts:         []app.Option{app.WithScheduleClock(clock.Now)},
	})
	s.automate(ctx, clock)
}

// enrollWithToken is the v0.2c path: an owner creates a one-time token over
// the API and the worker enrolls with it — a fresh key, a CSR, the CA
// pinned by the token's fingerprint. The token works exactly once.
func (s *scenario) enrollWithToken(ctx context.Context, managerAddr string) worker.Config {
	s.t.Helper()
	var created struct {
		Token string `json:"token"`
	}
	s.post("/api/v1/"+s.apiPath()+"/worker-enrollments", map[string]any{"zone_id": s.zoneID, "name": tokenWorkerName}, &created)
	if !strings.HasPrefix(created.Token, "pxw1.") {
		s.t.Fatalf("enrollment token = %q, want the pxw1 format", created.Token)
	}
	identityAt := func(dir string) worker.Config {
		return worker.Config{
			ManagerAddr: managerAddr,
			IdentityDir: dir,
			CertPath:    filepath.Join(dir, "cert.pem"),
			KeyPath:     filepath.Join(dir, "key.pem"),
			CAPath:      filepath.Join(dir, "ca.pem"),
			EnrollToken: created.Token,
			Version:     "e2e",
		}
	}
	cfg := identityAt(filepath.Join(s.t.TempDir(), "identity"))
	if err := worker.EnsureIdentity(ctx, cfg, testLogger(s.t)); err != nil {
		s.t.Fatalf("enroll with token: %v", err)
	}
	// Single use: a second enrollment with the same token is refused.
	if err := worker.EnsureIdentity(ctx, identityAt(filepath.Join(s.t.TempDir(), "again")), testLogger(s.t)); err == nil {
		s.t.Fatal("a second enrollment with the same token succeeded")
	}
	return cfg
}

// automate drives the v0.2c steps after the wiring: the worker list, a
// schedule firing on the injected clock, a signed webhook, and revocation.
func (s *scenario) automate(ctx context.Context, clock *fakeClock) {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	// --- The token-enrolled worker is listed and has claimed ---------------
	// The wiring's repository sync already ran on it, so it is online.
	var list workersOut
	s.get(prefix+"/workers", &list)
	if len(list.Workers) != 1 || list.Workers[0].Name != tokenWorkerName || list.Workers[0].Status != "online" {
		s.t.Fatalf("workers = %+v, want the one online token worker", list.Workers)
	}
	workerID := list.Workers[0].ID
	if exp := list.Workers[0].CertExpiresAt; exp == nil || time.Until(*exp) < 89*24*time.Hour || time.Until(*exp) > 91*24*time.Hour {
		s.t.Fatalf("certificate expiry = %v, want about 90 days", exp)
	}

	// --- Host key pin, playbook and template --------------------------------
	s.pinHostKey()
	s.helloPB = s.createEditorPlaybook("hello", greetTemplate(s.t))
	s.helloTID = s.createTemplate("hello", s.helloPB, map[string]any{"greeting": s.greeting})

	// --- A schedule fires on the injected clock -----------------------------
	var sched scheduleOut
	s.post(prefix+"/schedules", map[string]any{
		"template_id": s.helloTID, "name": "every five minutes", "mode": "cron",
		"cron": "*/5 * * * *", "timezone": "Europe/Berlin",
	}, &sched)
	if sched.Schedule.NextRunAt == nil {
		s.t.Fatal("the new schedule carries no next_run_at")
	}
	clock.setAt(*sched.Schedule.NextRunAt)
	fired := s.awaitScheduleRun(sched.Schedule.ID, 30*time.Second)
	if fired.Outcome != "triggered" || fired.JobGroupID == "" {
		s.t.Fatalf("schedule run = %+v, want triggered with a job group", fired)
	}
	var group jobListOut
	s.get(prefix+"/jobs?group="+fired.JobGroupID, &group)
	if len(group.Jobs) != 1 || group.Jobs[0].Trigger != "schedule" || group.Jobs[0].TriggerRef != sched.Schedule.ID {
		s.t.Fatalf("scheduled jobs = %+v, want one job triggered by schedule %s", group.Jobs, sched.Schedule.ID)
	}
	scheduled := s.awaitJob(group.Jobs[0].ID, "run", 90*time.Second)
	if scheduled.Job.Status != "succeeded" || scheduled.Job.WorkerName != tokenWorkerName {
		s.t.Fatalf("scheduled run = %q on %q (error %q), want succeeded on %s%s",
			scheduled.Job.Status, scheduled.Job.WorkerName, scheduled.Job.Error, tokenWorkerName, s.runnerOutput())
	}

	// --- A signed webhook starts a run ---------------------------------------
	var trig triggerCreatedOut
	s.post(prefix+"/triggers", map[string]any{
		"kind": "template_run", "template_id": s.helloTID, "name": "ci", "preset": "pulsix",
	}, &trig)
	runID := s.postHook(trig.URL, trig.Secret, []byte(`{"ref":"refs/heads/main"}`))
	hooked := s.awaitJob(runID, "run", 90*time.Second)
	if hooked.Job.Status != "succeeded" {
		s.t.Fatalf("webhook run = %q (error %q)%s", hooked.Job.Status, hooked.Job.Error, s.runnerOutput())
	}
	// A wrong signature answers the same uniform 404 as an unknown id.
	if code := s.postHookStatus(trig.URL, "sha256="+strings.Repeat("0", 64), []byte(`{}`)); code != http.StatusNotFound {
		s.t.Fatalf("hook with a wrong signature = %d, want 404", code)
	}

	// --- Revocation ------------------------------------------------------------
	var revoked struct {
		Worker struct {
			Status string `json:"status"`
		} `json:"worker"`
	}
	s.post(prefix+"/workers/"+workerID+"/revoke", struct{}{}, &revoked)
	if revoked.Worker.Status != "revoked" {
		s.t.Fatalf("revoked worker status = %q", revoked.Worker.Status)
	}
	// Its identity no longer authenticates: a direct claim is refused.
	conn, err := worker.Dial(ctx, s.workerCfg)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, err = workerpb.NewWorkerServiceClient(conn).ClaimJob(ctx, &workerpb.ClaimJobRequest{Version: "e2e", ZoneIds: []string{s.zoneID}})
	if status.Code(err) != codes.Unauthenticated {
		s.t.Fatalf("claim with a revoked certificate = %v, want Unauthenticated", err)
	}
	// With the zone's only worker revoked, a new run stays queued.
	queuedID := s.runTemplate(s.helloTID)
	time.Sleep(2 * time.Second) // twenty claim intervals of the (refused) loop
	var queued jobDetailOut
	s.get(prefix+"/jobs/"+queuedID, &queued)
	if queued.Job.Status != "queued" {
		s.t.Fatalf("run after revocation = %q, want queued", queued.Job.Status)
	}
	// A revoked worker can be deleted.
	s.api.do(http.MethodDelete, prefix+"/workers/"+workerID, nil, nil)
	s.get(prefix+"/workers", &list)
	if len(list.Workers) != 0 {
		s.t.Fatalf("workers after delete = %+v, want none", list.Workers)
	}
}

// pinHostKey runs one connection test and pins the host key it observed —
// runs refuse a host whose key is unknown.
func (s *scenario) pinHostKey() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	var started struct {
		JobID string `json:"job_id"`
	}
	s.post(prefix+"/hosts/"+s.hostID+"/connection-test", struct{}{}, &started)
	if job := s.awaitJob(started.JobID, "connection_test", 60*time.Second); job.Job.Status != "succeeded" {
		s.t.Fatalf("connection test = %q (error %q)", job.Job.Status, job.Job.Error)
	}
	var test hostTestOut
	s.get(prefix+"/hosts/"+s.hostID+"/connection-test", &test)
	s.put(prefix+"/hosts/"+s.hostID+"/host-key", map[string]any{"fingerprint": test.Test.HostKey}, &map[string]any{})
}

// awaitScheduleRun polls the schedule's run history until the ticker
// recorded its first decision.
func (s *scenario) awaitScheduleRun(id string, timeout time.Duration) scheduleRunOut {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var out struct {
			Runs []scheduleRunOut `json:"runs"`
		}
		s.get("/api/v1/"+s.apiPath()+"/schedules/"+id+"/runs", &out)
		if len(out.Runs) > 0 {
			return out.Runs[0]
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("schedule %s recorded no run in %s", id, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// postHook sends a correctly signed delivery and returns the started run.
func (s *scenario) postHook(url, secret string, body []byte) string {
	s.t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req := s.hookRequest(url, "sha256="+hex.EncodeToString(mac.Sum(nil)), body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		s.t.Fatalf("hook delivery = %d, want 202", resp.StatusCode)
	}
	var out hookOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.RunID == "" {
		s.t.Fatalf("hook delivery answered no run_id (%v)", err)
	}
	return out.RunID
}

// postHookStatus sends a delivery with the given signature header value.
func (s *scenario) postHookStatus(url, signature string, body []byte) int {
	s.t.Helper()
	resp, err := http.DefaultClient.Do(s.hookRequest(url, signature, body))
	if err != nil {
		s.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// hookRequest is a webhook delivery: no session cookie, only the signature.
func (s *scenario) hookRequest(url, signature string, body []byte) *http.Request {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pulsix-Signature-256", signature)
	return req
}
