package jobs_test

import (
	"crypto/rand"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestEventsSearchFiltersInSQL: a job's log read narrows by host, task
// substring, role prefix and state inside the database, pages by after and
// limit, treats % and _ in a search literally, and still returns an event
// a worker stamped a little before the job's creation (a lagging clock).
func TestEventsSearchFiltersInSQL(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-events-search")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)
	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	octx := db.WithOrgScope(ctx, org)
	job, err := svc.Get(octx, org, jobID)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	for _, e := range []jobs.Event{
		{Host: "web1", Task: "common : Install packages", State: "task"},                         // 1
		{Host: "web1", Task: "common : Install packages", State: "ok"},                           // 2
		{Host: "web1", Task: "Common : Install packages", State: "changed"},                      // 3
		{Host: "web2", Task: "nginx : Write 100%_config", State: "changed"},                      // 4
		{Host: "web2", Task: "nginx : Restart", State: "failed"},                                 // 5
		{State: "stdout", Payload: []byte("PLAY RECAP")},                                         // 6
		{Host: "db1", Task: "Gathering Facts", State: "unreachable"},                             // 7
		{Host: "web1", Task: "common_x : Install packages", State: "skipped"},                    // 8
		{Host: "web1", Task: "Write 100%_config", State: "changed"},                              // 9
		{Host: "late", Task: "clock : behind", State: "ok", At: job.CreatedAt.Add(-time.Minute)}, // 10
	} {
		if e.At.IsZero() {
			e.At = now
		}
		if err := queue.AppendEvent(ctx, jobID, e); err != nil {
			t.Fatalf("AppendEvent %+v: %v", e, err)
		}
	}

	for _, tc := range []struct {
		name string
		f    jobs.EventFilter
		want []uint64
	}{
		{name: "everything", want: []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
		{name: "first page", f: jobs.EventFilter{Limit: 3}, want: []uint64{1, 2, 3}},
		{name: "next page", f: jobs.EventFilter{After: 3, Limit: 3}, want: []uint64{4, 5, 6}},
		{name: "host", f: jobs.EventFilter{Host: "web2"}, want: []uint64{4, 5}},
		{name: "host is exact", f: jobs.EventFilter{Host: "web"}, want: nil},
		{name: "task substring, any case", f: jobs.EventFilter{Task: "INSTALL"}, want: []uint64{1, 2, 3, 8}},
		{name: "task percent is literal", f: jobs.EventFilter{Task: "%"}, want: []uint64{4, 9}},
		{name: "task underscore is literal", f: jobs.EventFilter{Task: "_"}, want: []uint64{4, 8, 9}},
		{name: "role prefix, any case", f: jobs.EventFilter{Role: "COMMON"}, want: []uint64{1, 2, 3}},
		{name: "role is the whole prefix", f: jobs.EventFilter{Role: "nginx"}, want: []uint64{4, 5}},
		{name: "role underscore is literal", f: jobs.EventFilter{Role: "commo_"}, want: nil},
		{name: "role percent is literal", f: jobs.EventFilter{Role: "%"}, want: nil},
		{name: "one state", f: jobs.EventFilter{States: []string{"changed"}}, want: []uint64{3, 4, 9}},
		{name: "several states", f: jobs.EventFilter{States: []string{"failed", "unreachable"}}, want: []uint64{5, 7}},
		{name: "combined", f: jobs.EventFilter{Host: "web1", Role: "common", States: []string{"changed", "ok"}}, want: []uint64{2, 3}},
		{name: "combined and paged", f: jobs.EventFilter{Host: "web1", Role: "common", States: []string{"changed", "ok"}, After: 2}, want: []uint64{3}},
		{name: "a lagging worker clock", f: jobs.EventFilter{Host: "late"}, want: []uint64{10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := svc.Events(octx, org, jobID, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			var got []uint64
			for _, e := range events {
				got = append(got, e.Seq)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("seqs %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAppendClampsAnEventStampedLongBeforeItsJob (backlog 2.138): both the
// append's max(seq) read and the log read are bounded at created_at minus
// EventClockSlack, so an event stored earlier than that would be invisible
// to both — the next append could reuse its seq and the log would hide it.
// The append clamps the stored time into the window and keeps the worker's
// own stamp in the payload.
func TestAppendClampsAnEventStampedLongBeforeItsJob(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-events-clamp")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)
	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	octx := db.WithOrgScope(ctx, org)
	job, err := svc.Get(octx, org, jobID)
	if err != nil {
		t.Fatal(err)
	}

	early := job.CreatedAt.Add(-2 * time.Hour)
	for _, e := range []jobs.Event{
		{Host: "a", Task: "first", State: "ok", At: time.Now()},
		{Host: "b", Task: "early", State: "ok", Payload: []byte(`{"k":1}`), At: early},
		{Host: "c", Task: "third", State: "ok", At: time.Now()},
	} {
		if err := queue.AppendEvent(ctx, jobID, e); err != nil {
			t.Fatalf("AppendEvent %+v: %v", e, err)
		}
	}

	events, err := svc.Events(octx, org, jobID, jobs.EventFilter{})
	if err != nil || len(events) != 3 {
		t.Fatalf("Events = %+v, %v; want all 3 events", events, err)
	}
	for i, e := range events {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d; want %d (unique, increasing)", i, e.Seq, i+1)
		}
	}
	floor := job.CreatedAt.Add(-jobs.EventClockSlack)
	if got := events[1].At; got.Before(floor.Add(-time.Millisecond)) || got.After(floor.Add(time.Millisecond)) {
		t.Fatalf("early event stored at %v; want clamped to %v", got, floor)
	}
	var p map[string]any
	if err := json.Unmarshal(events[1].Payload, &p); err != nil || p["k"] != float64(1) {
		t.Fatalf("early payload = %s, %v; want the worker's payload kept", events[1].Payload, err)
	}
	workerAt, _ := p["worker_at"].(string)
	if parsed, err := time.Parse(time.RFC3339Nano, workerAt); err != nil || !parsed.Equal(early) {
		t.Fatalf("worker_at = %q (%v); want the worker's stamp %v", workerAt, err, early)
	}
	for _, i := range []int{0, 2} {
		if strings.Contains(string(events[i].Payload), "worker_at") {
			t.Fatalf("unclamped event %d carries worker_at: %s", i, events[i].Payload)
		}
	}
}
