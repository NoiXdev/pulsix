package notify_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/notify"
)

type fakeDigests struct {
	mu       sync.Mutex
	settings *notify.DigestSettings
	channels []uuid.UUID
}

func (f *fakeDigests) GetDigest(context.Context, uuid.UUID) (notify.DigestSettings, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settings == nil {
		return notify.DigestSettings{}, false, nil
	}
	return *f.settings, true, nil
}

func (f *fakeDigests) PutDigest(_ context.Context, _ uuid.UUID, s notify.DigestSettings, channels []uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settings, f.channels = &s, channels
	return nil
}

func TestDigestSettings(t *testing.T) {
	store := &fakeDigests{}
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC) // a Tuesday
	svc := notify.NewService(notify.Deps{Digests: store, Now: func() time.Time { return now }})
	d, err := svc.Digest(ctx, uuid.Must(uuid.NewV7()))
	if err != nil || d.Enabled || notify.WeekdayName(d.Weekday) != "mon" || d.Time != "08:00" || d.Timezone != "UTC" {
		t.Fatalf("default = %+v, %v", d, err)
	}
	ch := uuid.Must(uuid.NewV7())
	d, err = svc.SetDigest(ctx, uuid.Must(uuid.NewV7()), notify.DigestInput{Enabled: true, Weekday: "Mon", Time: "08:00", Timezone: "Europe/Berlin", Channels: []uuid.UUID{ch, ch}})
	if err != nil || !d.Enabled || d.Weekday != 1 || d.NextRunAt == nil {
		t.Fatalf("set = %+v, %v", d, err)
	}
	// Monday 28 Sep 08:00 CEST is 06:00 UTC.
	if !d.NextRunAt.Equal(time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)) || len(store.channels) != 1 {
		t.Fatalf("next = %s, channels %v", d.NextRunAt, store.channels)
	}
	d, err = svc.SetDigest(ctx, uuid.Must(uuid.NewV7()), notify.DigestInput{Enabled: false, Weekday: "fri", Time: "17:30", Timezone: "UTC"})
	if err != nil || d.Enabled || d.NextRunAt != nil {
		t.Fatalf("disabled = %+v, %v", d, err)
	}
	for name, in := range map[string]notify.DigestInput{
		"bad weekday": {Weekday: "funday", Time: "08:00", Timezone: "UTC"},
		"bad time":    {Weekday: "mon", Time: "8am", Timezone: "UTC"},
		"late time":   {Weekday: "mon", Time: "24:00", Timezone: "UTC"},
		// fix round 1, Minor #1: fmt.Sscanf("%02d:%02d", ...) used to
		// accept a short match and leave the rest of the string
		// unchecked, so these all used to parse.
		"trailing junk":   {Weekday: "mon", Time: "08:0a", Timezone: "UTC"},
		"trailing junk 2": {Weekday: "mon", Time: "08:6a", Timezone: "UTC"},
		"leading sign":    {Weekday: "mon", Time: "+8:30", Timezone: "UTC"},
		"no timezone":     {Weekday: "mon", Time: "08:00"},
		"unknown zone":    {Weekday: "mon", Time: "08:00", Timezone: "Mars/Olympus"},
		"enabled, no ch":  {Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC"},
		"too many ch":     {Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: manyIDs(21)},
		"local time zone": {Weekday: "mon", Time: "08:00", Timezone: "Local"},
	} {
		if _, err := svc.SetDigest(ctx, uuid.Must(uuid.NewV7()), in); !errors.Is(err, notify.ErrInvalidInput) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func manyIDs(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.Must(uuid.NewV7())
	}
	return out
}

type fakeDigestQueue struct {
	due     []notify.DueDigest
	handled map[uuid.UUID]error
}

func (f *fakeDigestQueue) ProcessDue(ctx context.Context, _ time.Time, _ int, handle func(ctx context.Context, tx pgx.Tx, d notify.DueDigest) error) (int, error) {
	f.handled = map[uuid.UUID]error{}
	for _, d := range f.due {
		f.handled[d.OrgID] = handle(ctx, nil, d)
	}
	return len(f.due), nil
}

type fakeBuilder struct{ err error }

func (b fakeBuilder) Digest(_ context.Context, _ uuid.UUID, from, to time.Time) (notify.DigestSummary, error) {
	return notify.DigestSummary{From: from, To: to}, b.err
}

type recordingDigestOutbox struct {
	sums map[uuid.UUID]notify.DigestSummary
}

func (o *recordingDigestOutbox) EnqueueDigest(_ context.Context, _ pgx.Tx, d notify.DueDigest, sum notify.DigestSummary) error {
	o.sums[d.OrgID] = sum
	return nil
}

func TestDigestTicker(t *testing.T) {
	now := time.Date(2026, 9, 28, 6, 0, 30, 0, time.UTC)
	onTime, late, broken := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	q := &fakeDigestQueue{due: []notify.DueDigest{
		{OrgID: onTime, Occurrence: now.Add(-30 * time.Second), Timezone: "Europe/Berlin"},
		{OrgID: late, Occurrence: now.Add(-49 * time.Hour), Timezone: "UTC"},
	}}
	outbox := &recordingDigestOutbox{sums: map[uuid.UUID]notify.DigestSummary{}}
	tk := notify.NewDigestTicker(q, fakeBuilder{}, outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	if n, err := tk.Tick(ctx); err != nil || n != 2 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	sum, ok := outbox.sums[onTime]
	if !ok || sum.Timezone != "Europe/Berlin" || !sum.To.Equal(now.Add(-30*time.Second)) || !sum.From.Equal(sum.To.AddDate(0, 0, -7)) {
		t.Fatalf("on-time digest = %+v, %v", sum, ok)
	}
	// A late digest reports ErrDigestSkipped rather than nil since
	// backlog 2.82: the store has to tell "we passed this week over"
	// from "we sent it", because only the second may stamp last_period.
	if _, sent := outbox.sums[late]; sent || !errors.Is(q.handled[late], notify.ErrDigestSkipped) {
		t.Fatalf("a digest two days late must be skipped (and advanced), got sent=%v err=%v", sent, q.handled[late])
	}
	q.due = []notify.DueDigest{{OrgID: broken, Occurrence: now}}
	failing := notify.NewDigestTicker(q, fakeBuilder{err: errors.New("db down")}, outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	if _, err := failing.Tick(ctx); err != nil || q.handled[broken] == nil {
		t.Fatalf("a builder failure must keep the digest due: handled %v", q.handled[broken])
	}
	if got := (notify.DueDigest{Occurrence: time.Date(2026, 9, 28, 8, 0, 0, 0, time.FixedZone("CEST", 7200))}).Key(); got != "digest:2026-09-28T06:00:00Z" {
		t.Fatalf("key = %s", got)
	}
}

// TestDigestWeekBoundaryCrossesDST is the dispatch's own must-hold (not
// the brief's — see the report's fix-round-1 note): the digest's from/to
// bounds are computed in the org's timezone, with DST
// handled with the same care cronexpr gives a schedule's own occurrences.
// Monday 26 Oct 2026 08:00 Europe/Berlin (CET, UTC+1) is 07:00 UTC; the
// switch out of summer time happened the day before, on 25 Oct. The
// previous Monday, 19 Oct, was still CEST (UTC+2): 08:00 CEST is 06:00
// UTC — one hour earlier than naively subtracting exactly seven 24-hour
// days from the occurrence (07:00 UTC) would give. A one-hour drift here
// would silently exclude a run at the very moment the previous digest
// itself fired.
func TestDigestWeekBoundaryCrossesDST(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	occ := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	now := occ.Add(30 * time.Second)
	q := &fakeDigestQueue{due: []notify.DueDigest{{OrgID: org, Occurrence: occ, Timezone: "Europe/Berlin"}}}
	outbox := &recordingDigestOutbox{sums: map[uuid.UUID]notify.DigestSummary{}}
	tk := notify.NewDigestTicker(q, fakeBuilder{}, outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	if _, err := tk.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	if sum := outbox.sums[org]; !sum.From.Equal(want) {
		t.Fatalf("from = %s, want %s (the DST-correct previous Monday 08:00 Europe/Berlin)", sum.From, want)
	}
}

// cancellingBuilder stands in for a build that runs right up to the batch
// deadline: it ends the tick's own context from inside the build, exactly
// as the deadline would.
type cancellingBuilder struct{ cancel context.CancelFunc }

func (b cancellingBuilder) Digest(_ context.Context, _ uuid.UUID, from, to time.Time) (notify.DigestSummary, error) {
	b.cancel()
	return notify.DigestSummary{From: from, To: to}, nil
}

// ctxDigestOutbox records the shape of the context its enqueue was handed.
type ctxDigestOutbox struct {
	live     bool
	bounded  bool
	enqueued int
}

func (o *ctxDigestOutbox) EnqueueDigest(ctx context.Context, _ pgx.Tx, _ notify.DueDigest, _ notify.DigestSummary) error {
	o.enqueued++
	o.live = ctx.Err() == nil
	_, o.bounded = ctx.Deadline()
	return nil
}

// TestDigestTickEnqueuesOnADetachedContext pins the split the batch
// deadline depends on. The build runs on the batch context — it issues
// its statements on other pool connections, and that wait is what the
// deadline exists to bound — but the enqueue writes on the batch
// transaction's own connection, and pgx aborts an in-flight statement by
// poisoning its connection. An enqueue left on the batch context would
// therefore destroy the connection ProcessDue still has to commit on
// whenever the deadline landed mid-INSERT, failing the whole batch
// ("commit digest tick: conn closed") instead of only the late row. It
// still has to be bounded, just not by the batch's clock.
func TestDigestTickEnqueuesOnADetachedContext(t *testing.T) {
	now := time.Date(2026, 9, 28, 6, 0, 30, 0, time.UTC)
	org := uuid.Must(uuid.NewV7())
	q := &fakeDigestQueue{due: []notify.DueDigest{{OrgID: org, Occurrence: now.Add(-30 * time.Second), Timezone: "UTC"}}}
	outbox := &ctxDigestOutbox{}
	tickCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tk := notify.NewDigestTicker(q, cancellingBuilder{cancel: cancel}, outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	if _, err := tk.Tick(tickCtx); err != nil {
		t.Fatal(err)
	}
	if outbox.enqueued != 1 || q.handled[org] != nil {
		t.Fatalf("enqueued %d times, handle = %v, want one clean enqueue", outbox.enqueued, q.handled[org])
	}
	if !outbox.live {
		t.Error("the digest enqueue ran on the batch context; a deadline mid-INSERT would take the batch transaction's connection down with it")
	}
	if !outbox.bounded {
		t.Error("the digest enqueue ran on an unbounded context; an unresponsive database would pin the batch forever")
	}
}
