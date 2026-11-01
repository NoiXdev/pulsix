// The run view's SSE stream tests (backlog 2.12), at the HTTP level:
// subscribe through a real request, feed fake events through the real
// queue's append path, and assert the frame order and format, the
// terminal status ending the stream, the late subscriber's
// current-status-first delivery, and that a cancelled HTTP request
// unsubscribes without leaving a subscriber behind.
package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// sseFrame is one parsed frame of the stream.
type sseFrame struct {
	event string
	data  string
}

// readSSEFrame reads one frame: its event line, its data line, and the
// blank line that closes it — the exact wire format the stream writes.
func readSSEFrame(t *testing.T, br *bufio.Reader) sseFrame {
	t.Helper()
	var fr sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v (after %q/%q)", err, fr.event, fr.data)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			return fr
		case strings.HasPrefix(line, "event: "):
			fr.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			fr.data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("malformed SSE line %q", line)
		}
	}
}

// sseStatusData decodes a status frame's data payload.
type sseStatusData struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// sseLogData decodes a log frame's data: the event the worker appended.
type sseLogData struct {
	Seq     uint64         `json:"seq"`
	Host    string         `json:"host"`
	Task    string         `json:"task"`
	State   string         `json:"state"`
	Payload map[string]any `json:"payload"`
}

// openStream opens the SSE endpoint as client, with ctx governing the
// request's lifetime (a cancel IS the client disconnect).
func openStream(t *testing.T, client *http.Client, url string, ctx context.Context) (*bufio.Reader, *http.Response) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open stream: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("cache control = %q, want no-cache", cc)
	}
	return bufio.NewReader(resp.Body), resp
}

func TestJobStreamFramesInOrder(t *testing.T) {
	f := newRunFixture(t, "run-sse-org")
	jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)

	// The worker claims the job first: the subscriber joins a RUNNING
	// run, and every event it appends lands on the stream.
	claim, err := f.s.Queue.Claim(context.Background(), f.orgID, []uuid.UUID{f.zoneA}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim: %v, %v", claim, err)
	}
	if id := claim.Job.GetId(); id != jobID.String() {
		t.Fatalf("claimed %s, want %s", id, jobID)
	}

	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	br, _ := openStream(t, f.adminC, f.baseURL()+"/"+jobID.String()+"/stream", sctx)

	// The stream opens with the CURRENT status (running), not a replay.
	first := readSSEFrame(t, br)
	if first.event != "status" {
		t.Fatalf("first frame = %q, want status", first.event)
	}
	var st sseStatusData
	if err := json.Unmarshal([]byte(first.data), &st); err != nil {
		t.Fatal(err)
	}
	if st.ID != jobID.String() || st.Status != "running" {
		t.Fatalf("status frame = %+v, want the running job", st)
	}

	// Feed three events through the queue's append path — exactly what
	// the gRPC EventRecorder's Append calls do — and expect them on the
	// stream, in order, as log frames.
	for i, ev := range []jobs.Event{
		{Seq: 1, Host: "web-1", Task: "Gathering Facts", State: "ok", Payload: json.RawMessage(`{"delta":1}`), At: time.Now()},
		{Seq: 2, Host: "web-1", Task: "deploy", State: "changed", Payload: nil, At: time.Now()},
		{Seq: 3, Host: "web-1", Task: "deploy", State: "ok", Payload: json.RawMessage(`"done"`), At: time.Now()},
	} {
		if err := f.s.Queue.AppendEvent(context.Background(), jobID, ev); err != nil {
			t.Fatalf("AppendEvent %d: %v", ev.Seq, err)
		}
		frame := readSSEFrame(t, br)
		if frame.event != "log" {
			t.Fatalf("frame %d event = %q, want log", i, frame.event)
		}
		var got sseLogData
		if err := json.Unmarshal([]byte(frame.data), &got); err != nil {
			t.Fatal(err)
		}
		if got.Seq != uint64(i+1) {
			t.Fatalf("frame %d seq = %d, want in order", i, got.Seq)
		}
	}

	// The worker reports the terminal result: the stream reports it and
	// ends.
	if err := f.s.Queue.Finish(context.Background(), jobID, jobs.StatusFailed, map[string]any{"ok": 1}, "boom"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	last := readSSEFrame(t, br)
	if last.event != "status" {
		t.Fatalf("final frame event = %q, want status", last.event)
	}
	if err := json.Unmarshal([]byte(last.data), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "failed" {
		t.Fatalf("final status = %q, want failed", st.Status)
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream must end after the terminal status: %v", err)
	}
}

// TestJobStreamLateSubscriberGetsCurrentStatusFirst is the brief's
// late-subscriber rule: a subscriber that joins a running run receives
// the CURRENT status first and then only the events appended after it
// joined — never a replay of the ones it missed.
func TestJobStreamLateSubscriberGetsCurrentStatusFirst(t *testing.T) {
	f := newRunFixture(t, "run-sse-late-org")
	jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)

	// Two events land BEFORE anyone subscribes.
	appendEvents(t, f.s, f.zoneA, jobID, []jobs.Event{
		{Seq: 1, Host: "web-1", Task: "Gathering Facts", State: "ok", Payload: nil, At: time.Now()},
		{Seq: 2, Host: "web-1", Task: "deploy", State: "ok", Payload: nil, At: time.Now()},
	})

	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	br, _ := openStream(t, f.adminC, f.baseURL()+"/"+jobID.String()+"/stream", sctx)

	// First frame: the current status, so the UI renders immediately.
	var st sseStatusData
	if err := json.Unmarshal([]byte(readSSEFrame(t, br).data), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "running" {
		t.Fatalf("initial status = %q, want running", st.Status)
	}

	// New events only: seq 3 arrives, and the pre-join seqs 1 and 2
	// never do.
	if err := f.s.Queue.AppendEvent(context.Background(), jobID, jobs.Event{Seq: 3, Host: "web-1", State: "ok", Payload: nil, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var log1 sseLogData
	if err := json.Unmarshal([]byte(readSSEFrame(t, br).data), &log1); err != nil {
		t.Fatal(err)
	}
	if log1.Seq != 3 {
		t.Fatalf("first log frame seq = %d, want 3 (only new events)", log1.Seq)
	}

	// The terminal finish ends the stream.
	if err := f.s.Queue.Finish(context.Background(), jobID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	last := readSSEFrame(t, br)
	if last.event != "status" {
		t.Fatalf("final frame event = %q, want status", last.event)
	}
	if err := json.Unmarshal([]byte(last.data), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "succeeded" {
		t.Fatalf("final status = %q, want succeeded", st.Status)
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream must end after the terminal status: %v", err)
	}
}

// TestJobStreamAlreadyTerminalIsOneFrame proves subscribing to a
// finished run: the current (terminal) status frame, then the stream
// ends at once.
func TestJobStreamAlreadyTerminalIsOneFrame(t *testing.T) {
	f := newRunFixture(t, "run-sse-done-org")
	jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)
	finishJob(t, f.s, f.zoneA, jobID, jobs.StatusCancelled, nil, "")

	br, _ := openStream(t, f.adminC, f.baseURL()+"/"+jobID.String()+"/stream", context.Background())
	var st sseStatusData
	if err := json.Unmarshal([]byte(readSSEFrame(t, br).data), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", st.Status)
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream must end right after the terminal status: %v", err)
	}
}

// TestJobStreamDisconnectUnsubscribes is the no-leak rule: a client
// that disconnects mid-stream unsubscribes — the broker's subscriber
// count for the job returns to zero.
func TestJobStreamDisconnectUnsubscribes(t *testing.T) {
	f := newRunFixture(t, "run-sse-leak-org")
	jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)
	if got := f.s.Deps.Jobs.EventsBroker().Len(jobID); got != 0 {
		t.Fatalf("broker len before any subscriber = %d, want 0", got)
	}

	rctx, rcancel := context.WithCancel(context.Background())
	defer rcancel()
	_, resp := openStream(t, f.adminC, f.baseURL()+"/"+jobID.String()+"/stream", rctx)
	defer func() { _ = resp.Body.Close() }()

	// The subscriber is live: one entry in the registry.
	deadline := time.Now().Add(5 * time.Second)
	for f.s.Deps.Jobs.EventsBroker().Len(jobID) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("subscriber never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The client disconnects (cancelling the request's context is
	// exactly what a browser tab closing does).
	rcancel()

	// The handler must unsubscribe on its way out: the count returns to
	// zero, nothing is left behind.
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got := f.s.Deps.Jobs.EventsBroker().Len(jobID); got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriber leaked: %d left after disconnect", f.s.Deps.Jobs.EventsBroker().Len(jobID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestJobStreamAuthMatrix proves the stream is a read: any member can
// stream it (viewer included), an anonymous caller cannot, and an
// unknown job is 404 before any frame is written.
func TestJobStreamAuthMatrix(t *testing.T) {
	f := newRunFixture(t, "run-sse-auth-org")
	jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)
	streamURL := f.baseURL() + "/" + jobID.String() + "/stream"

	// Viewer: streaming is a read.
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_, vresp := openStream(t, f.viewerC, streamURL, sctx)
	_ = vresp.Body.Close()

	// Anonymous: 401 with no frame written.
	resp := requestJSON(t, f.s.Client(), http.MethodGet, streamURL, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous stream: status %d, want 401", resp.StatusCode)
	}

	// An unknown job id: 404, the normal JSON error shape.
	resp = requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"/"+uuid.Must(uuid.NewV7()).String()+"/stream", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown job's stream: status %d, want 404", resp.StatusCode)
	}
}

// TestJobStreamEndsWhenAccessEnds is backlog 7.22: the stream authorised
// once and then ran until the job finished, so a member removed from the
// organization, or a session signed out elsewhere, kept reading the run's
// output. It now re-checks the caller's access while it runs and ends with
// one error frame once that access is gone.
func TestJobStreamEndsWhenAccessEnds(t *testing.T) {
	cases := map[string]func(f runFixture, userID uuid.UUID, c *http.Client){
		"removed from the organization": func(f runFixture, userID uuid.UUID, _ *http.Client) {
			resp := requestJSON(t, f.adminC, http.MethodDelete, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/members/"+userID.String(), nil)
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
				t.Fatalf("remove member: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
			}
		},
		"signed out": func(f runFixture, _ uuid.UUID, c *http.Client) {
			resp := postJSON(t, c, f.s.HTTP.URL+"/api/v1/auth/logout", nil)
			if resp.StatusCode/100 != 2 {
				t.Fatalf("logout: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
			}
		},
	}
	for name, revoke := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRunFixture(t, "run-sse-revoke-"+strings.ReplaceAll(name, " ", "-"))
			jobID := mustUUID(t, runJob(t, f.operatorC, f.runURL())[0].ID)
			userID := f.s.SeedUser("sse-watcher-"+strings.ReplaceAll(name, " ", "-")+"@example.test", "correct horse battery")
			f.s.AddMember(f.orgID, userID, tenancy.RoleViewer)
			c := f.s.LoginAs(userID)

			sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer scancel()
			br, resp := openStream(t, c, f.baseURL()+"/"+jobID.String()+"/stream", sctx)
			defer func() { _ = resp.Body.Close() }()
			if first := readSSEFrame(t, br); first.event != "status" {
				t.Fatalf("first frame = %q, want status", first.event)
			}

			revoke(f, userID, c)

			// The job is still running, so only the access check can end
			// the stream: one error frame, then EOF. A log frame may still
			// arrive within one re-check interval; nothing else may.
			for {
				fr := readSSEFrame(t, br)
				if fr.event == "error" {
					break
				}
				if fr.event != "log" {
					t.Fatalf("frame %q before the stream ended, want error", fr.event)
				}
			}
			if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("stream must end after access ended: %v", err)
			}
		})
	}
}
