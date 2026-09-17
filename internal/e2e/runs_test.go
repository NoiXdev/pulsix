// The scenario's run flow: the connection test through the bastion and its
// pin, the golden inventory compare, the successful run with its live event
// stream, and the failure path with retry-failed through the template
// update that flips the outcome var.
package e2e

import (
	"encoding/json"

	"os"
	"strings"
	"testing"
	"time"
)

// The fixture playbooks' task names — the same names the real playbooks
// carry, so the fake runner's events and the real ansible's events read
// the same to the assertions.
const (
	taskGreeting = "Pulsix greeting"
	taskMarker   = "Pulsix marker"
	taskFailure  = "Pulsix deliberate failure"
)

// drive is the scenario's run half.
func (s *scenario) drive() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()

	// --- The connection test through the bastion ------------------------
	var testOut struct {
		JobID string `json:"job_id"`
	}
	s.post(prefix+"/hosts/"+s.hostID+"/connection-test", struct{}{}, &testOut)
	job := s.awaitJob(testOut.JobID, "connection_test", 60*time.Second)
	if job.Job.Status != "succeeded" {
		s.t.Fatalf("connection test job = %q (error %q)", job.Job.Status, job.Job.Error)
	}
	if got := s.bastionS.directTCP; got != 1 {
		s.t.Fatalf("bastion direct-tcpip opens = %d, want 1 — the route was not used", got)
	}

	var test hostTestOut
	s.get(prefix+"/hosts/"+s.hostID+"/connection-test", &test)
	if !test.Test.OK {
		s.t.Fatalf("connection test ok = false: %+v", test.Test)
	}
	// Every stage held: reachability (through the bastion), host key, auth,
	// passwordless sudo and Python presence (spec §5).
	for _, c := range test.Test.Checks {
		if !c.OK {
			s.t.Fatalf("connection test stage %q failed: %q", c.Name, c.Detail)
		}
	}
	// The reachability detail names the route: "(via <hop>)", never the
	// direct path.
	if !strings.Contains(strings.Join(checkDetails(test), " "), "(via ") {
		s.t.Fatalf("the reachability detail does not name the bastion route: %+v", test.Test.Checks)
	}
	if test.Test.HostKeyState != "unpinned" {
		s.t.Fatalf("host key state = %q, want unpinned (trust on first use)", test.Test.HostKeyState)
	}
	if !strings.HasPrefix(test.Test.HostKey, "SHA256:") {
		s.t.Fatalf("observed host key = %q, want an OpenSSH SHA256 fingerprint", test.Test.HostKey)
	}

	// Pin what the test saw.
	s.put(prefix+"/hosts/"+s.hostID+"/host-key", map[string]any{"fingerprint": test.Test.HostKey}, &map[string]any{})

	// The second test honors the pin: the verdict reads pinned now.
	var second struct {
		JobID string `json:"job_id"`
	}
	s.post(prefix+"/hosts/"+s.hostID+"/connection-test", struct{}{}, &second)
	s.awaitJob(second.JobID, "connection_test", 60*time.Second)
	var pinned hostTestOut
	s.get(prefix+"/hosts/"+s.hostID+"/connection-test", &pinned)
	if pinned.Test.HostKeyState != "pinned" {
		s.t.Fatalf("host key state after pin = %q, want pinned", pinned.Test.HostKeyState)
	}
	if pinned.Test.HostKey != test.Test.HostKey {
		s.t.Fatal("the second test observed a different host key than the pin")
	}

	// --- The golden inventory (the job's scope, masked) ------------------
	s.checkGoldenInventory()

	// --- The successful run ----------------------------------------------
	s.helloPB = s.createEditorPlaybook("hello", greetTemplate(s.t))
	s.helloTID = s.createTemplate("hello", s.helloPB, map[string]any{
		"greeting": s.greeting,
	})
	runID := s.runTemplate(s.helloTID)
	streamDone := make(chan sseResult, 1)
	go func() {
		frames, err := s.sseStream(runID, 30*time.Second)
		streamDone <- sseResult{frames: frames, err: err}
	}()
	run := s.awaitJob(runID, "run", 90*time.Second)
	if run.Job.Status != "succeeded" {
		s.t.Fatalf("run job = %q (error %q)%s", run.Job.Status, run.Job.Error, s.runnerOutput())
	}
	// The claim records the claiming worker (worker_id binds the result to
	// it), so the detail names the enrolled worker.
	if run.Job.WorkerName != "e2e-worker" {
		s.t.Fatalf("run worker_name = %q, want the claiming worker e2e-worker", run.Job.WorkerName)
	}
	if run.Job.Template == nil || run.Job.Template.Name != "hello" {
		s.t.Fatalf("run template = %+v, want the hello template", run.Job.Template)
	}
	s.assertRunStats(run)
	// Wait for the stream to have flushed completely (the run_end bookend)
	// before asserting on it.
	events := s.awaitEvents(runID, 30*time.Second, func(e jobEventsOut) bool {
		return hasEvent(e, "ok", taskGreeting) && hasEvent(e, "run_end", "")
	})
	s.assertRunEvents(events)

	// The live stream: the frames a subscriber saw while the run executed.
	// The collector goroutine hands back an error with the frames, so a
	// stuck or broken stream fails the test here instead of hanging.
	collected := <-streamDone
	if collected.err != nil {
		s.t.Fatalf("collect SSE stream for job %s: %v", runID, collected.err)
	}
	s.assertSSE(collected.frames, "succeeded")

	// The failure path uses the same stream contract; the retry below
	// covers the second template's runs.
	// --- The failure path --------------------------------------------------
	s.failPB = s.createEditorPlaybook("fail", failTemplate(s.t))
	s.failTID = s.createTemplate("deliberate", s.failPB, map[string]any{
		"should_fail": "true",
	})
	failID := s.runTemplate(s.failTID)
	failed := s.awaitJob(failID, "run", 90*time.Second)
	if failed.Job.Status != "failed" {
		s.t.Fatalf("deliberate failure run = %q (error %q), want failed%s", failed.Job.Status, failed.Job.Error, s.runnerOutput())
	}
	hostUUID := s.hostID
	failedList, _ := failed.Job.Stats["failed_hosts"].([]any)
	if len(failedList) != 1 || failedList[0] != hostUUID {
		s.t.Fatalf("failed run stats failed_hosts = %v, want [%s]", failedList, hostUUID)
	}
	s.awaitEvents(failID, 30*time.Second, func(e jobEventsOut) bool {
		return hasEvent(e, "failed", taskFailure) && hasEvent(e, "run_end", "")
	})

	// --- The retry: flip the var through the template update, retry -------
	s.put("/api/v1/"+s.apiPath()+"/job-templates/"+s.failTID, map[string]any{
		"name": "deliberate", "playbook_id": s.failPB,
		"extra_vars": map[string]any{"should_fail": "false"},
	}, &templateOut{})
	retryOut := s.retryFailed(failID)
	retry := s.awaitJob(retryOut.Jobs[0].ID, "run", 90*time.Second)
	if retry.Job.Status != "succeeded" {
		s.t.Fatalf("retry run = %q (error %q)%s", retry.Job.Status, retry.Job.Error, s.runnerOutput())
	}
	// The retry's scope is only what failed before: this one host.
	if len(retry.Job.Hosts) != 1 || retry.Job.Hosts[0] != hostUUID {
		s.t.Fatalf("retry hosts = %v, want [%s]", retry.Job.Hosts, hostUUID)
	}

	s.hostKeyChanges()
}

// hostKeyChanges is the pin's failure case (final review C2): the host
// starts presenting a different host key after it was pinned. A run that
// is claimed before any connection test noticed must still refuse to talk
// to the host — the worker's known_hosts pins the key on ansible's own
// connection, through the bastion — and once a test reports the change,
// runs and ad-hoc commands are refused up front.
func (s *scenario) hostKeyChanges() {
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	s.host.rekey(s.t)

	runID := s.runTemplate(s.helloTID)
	run := s.awaitJob(runID, "run", 90*time.Second)
	if run.Job.Status == "succeeded" {
		s.t.Fatalf("a run against a host whose key no longer matches its pin succeeded (error %q)", run.Job.Error)
	}
	unreachable, _ := run.Job.Stats["unreachable_hosts"].([]any)
	if run.Job.Status != "failed" || len(unreachable) != 1 || unreachable[0] != s.hostID {
		s.t.Fatalf("changed-key run = %q, unreachable_hosts %v (error %q); want failed with the host unreachable%s",
			run.Job.Status, run.Job.Stats["unreachable_hosts"], run.Job.Error, s.runnerOutput())
	}
	events := s.awaitEvents(runID, 30*time.Second, func(e jobEventsOut) bool { return hasEvent(e, "run_end", "") })
	sawUnreachable := false
	for _, ev := range events.Events {
		if ev.State == "unreachable" {
			sawUnreachable = true
			if testing.Verbose() {
				raw, _ := json.Marshal(ev.Payload)
				s.t.Logf("changed-key run: unreachable event %s", raw)
			}
		}
	}
	if !sawUnreachable {
		s.t.Fatalf("the changed-key run streamed no unreachable event: %+v", events.Events)
	}

	// A connection test sees the change and records it.
	var testOut struct {
		JobID string `json:"job_id"`
	}
	s.post(prefix+"/hosts/"+s.hostID+"/connection-test", struct{}{}, &testOut)
	s.awaitJob(testOut.JobID, "connection_test", 60*time.Second)
	var changed hostTestOut
	s.get(prefix+"/hosts/"+s.hostID+"/connection-test", &changed)
	if changed.Test.HostKeyState != "changed" {
		s.t.Fatalf("host key state after the key change = %q, want changed", changed.Test.HostKeyState)
	}

	// Now every SSH-bound job for the host is refused up front.
	if code := s.api.status("POST", prefix+"/job-templates/"+s.helloTID+"/run", struct{}{}); code != 409 {
		s.t.Fatalf("run against a changed host key = %d, want 409", code)
	}
	if code := s.api.status("POST", prefix+"/hosts/command", map[string]any{
		"host_ids": []string{s.hostID}, "module": "shell", "argv": "true",
	}); code != 409 {
		s.t.Fatalf("ad-hoc command against a changed host key = %d, want 409", code)
	}
}

// checkDetails joins the connection test's per-stage details.
func checkDetails(test hostTestOut) []string {
	out := make([]string, 0, len(test.Test.Checks))
	for _, c := range test.Test.Checks {
		out = append(out, c.Detail)
	}
	return out
}

// hasEvent reports whether events carry one with the given state and task.
func hasEvent(e jobEventsOut, state, task string) bool {
	for _, ev := range e.Events {
		if ev.State == state && ev.Task == task {
			return true
		}
	}
	return false
}

// assertRunStats checks the run's per-host stats contract: the recap mapped
// onto the claim's connections, so the lists carry the HOST UUID.
func (s *scenario) assertRunStats(run jobDetailOut) {
	s.t.Helper()
	hostUUID := s.hostID
	if ok, _ := run.Job.Stats["ok_hosts"].([]any); len(ok) != 1 || ok[0] != hostUUID {
		s.t.Fatalf("run stats ok_hosts = %v, want [%s]", run.Job.Stats["ok_hosts"], hostUUID)
	}
	if ch, _ := run.Job.Stats["changed_hosts"].([]any); len(ch) != 1 || ch[0] != hostUUID {
		s.t.Fatalf("run stats changed_hosts = %v, want [%s]", run.Job.Stats["changed_hosts"], hostUUID)
	}
	if f, _ := run.Job.Stats["failed_hosts"].([]any); len(f) != 0 {
		s.t.Fatalf("run stats failed_hosts = %v, want empty", f)
	}
}

// assertRunEvents checks the event stream the run produced: the bookends,
// the fixture's final task ok, and the greeting the extra var carried.
func (s *scenario) assertRunEvents(e jobEventsOut) {
	s.t.Helper()
	if len(e.Events) == 0 {
		s.t.Fatal("the run produced no events")
	}
	first, last := e.Events[0], e.Events[len(e.Events)-1]
	if first.State != "run_start" {
		s.t.Fatalf("first event state = %q, want run_start", first.State)
	}
	if last.State != "run_end" {
		raw, _ := json.Marshal(e.Events)
		s.t.Fatalf("last event state = %q, want run_end; all events: %s", last.State, raw)
	}
	if !hasEvent(e, "ok", taskGreeting) {
		s.t.Fatalf("no ok event for %q: %+v", taskGreeting, e.Events)
	}
	// The raw payload of the ok event carries the greeting (the template's
	// extra var reached the run).
	var greeting string
	for _, ev := range e.Events {
		if ev.State == "ok" && ev.Task == taskGreeting {
			if raw, err := json.Marshal(ev.Payload); err == nil {
				greeting = string(raw)
			}
		}
	}
	if !strings.Contains(greeting, s.greeting) {
		s.t.Fatalf("the ok event payload does not carry the greeting %q: %s", s.greeting, greeting)
	}
}

// assertSSE checks the live stream's shape: a status frame first, log
// frames in between, and the terminal status frame last.
func (s *scenario) assertSSE(frames []sseFrame, terminal string) {
	s.t.Helper()
	if testing.Verbose() {
		for _, f := range frames {
			s.t.Logf("SSE %s %s", f.Event, f.Data)
		}
	}
	if len(frames) == 0 {
		s.t.Fatal("the SSE stream produced no frames")
	}
	if frames[0].Event != "status" {
		s.t.Fatalf("first SSE frame = %q, want status", frames[0].Event)
	}
	var lastStatus string
	for _, f := range frames {
		if f.Event == "status" {
			var body struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(f.Data), &body); err != nil {
				s.t.Fatalf("decode SSE status frame: %v", err)
			}
			lastStatus = body.Status
		}
	}
	if lastStatus != terminal {
		s.t.Fatalf("last SSE status = %q, want %q", lastStatus, terminal)
	}
	logs := 0
	for _, f := range frames {
		if f.Event == "log" {
			logs++
		}
	}
	if logs == 0 {
		s.t.Fatal("the SSE stream carried no log frames")
	}
}

// checkGoldenInventory renders the job's scope through the masked preview
// endpoint and compares it against the checked-in golden fixture.
func (s *scenario) checkGoldenInventory() {
	s.t.Helper()
	var out previewOut
	s.get("/api/v1/"+s.apiPath()+"/inventory/preview?group="+s.groupID, &out)
	raw, err := json.Marshal(out.Inventory)
	if err != nil {
		s.t.Fatal(err)
	}
	normalized, err := NormalizeInventory(raw)
	if err != nil {
		s.t.Fatal(err)
	}

	// The connection the preview resolves: the host rides the zone default
	// bastion, whose id and credential the connection names (ids only).
	conn, ok := out.Connections[hostName]
	if !ok {
		s.t.Fatalf("preview carries no connection for %s", hostName)
	}
	if conn["bastion_source"] != "zone" {
		s.t.Fatalf("connection bastion_source = %v, want zone", conn["bastion_source"])
	}
	if hops, _ := conn["bastions"].([]any); len(hops) != 1 || hops[0] != s.bastionID {
		s.t.Fatalf("connection bastions = %v, want [%s]", hops, s.bastionID)
	}

	if os.Getenv("PULSIX_E2E_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(GoldenPath, []byte(normalized+"\n"), 0o644); err != nil {
			s.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(GoldenPath)
	if err != nil {
		s.t.Fatalf("read golden fixture: %v", err)
	}
	got := strings.TrimSpace(normalized)
	if got != strings.TrimSpace(string(want)) {
		s.t.Fatalf("the inventory render does not match the golden fixture\n got: %s\nwant: %s", got, string(want))
	}
}
