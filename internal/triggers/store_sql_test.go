package triggers_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/triggers"
)

// seedTemplate creates an org, an editor playbook and a template through
// the raw pool (fixtures bypass RLS).
func seedTemplate(t *testing.T, d *db.DB, slug string) (org, templateID uuid.UUID) {
	t.Helper()
	q := gen.New(d.Pool())
	o, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	inline := "- hosts: all\n  tasks: []\n"
	pb, err := q.CreatePlaybook(ctx, gen.CreatePlaybookParams{ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Inline: &inline, Source: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := q.InsertTemplate(ctx, gen.InsertTemplateParams{ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Name: "deploy", PlaybookID: db.NullUUID(&pb.ID), Scope: []byte(`{}`), ExtraVars: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID, tmpl.ID
}

func storedTrigger(org, templateID uuid.UUID, rl triggers.RateLimit) triggers.Trigger {
	return triggers.Trigger{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: triggers.KindTemplateRun, TemplateID: &templateID,
		Name: "t-" + uuid.NewString()[:8], Enabled: true, Preset: triggers.PresetPulsix, OnActive: triggers.OnActiveQueue, RateLimit: rl}
}

func TestStoreRoundTripAndResolveWithoutScope(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-store")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit)
	created, err := st.Create(scoped, in, []byte("sealed"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Auth() != triggers.AuthHMAC {
		t.Fatalf("created = %+v", created)
	}
	// The hook path: no org scope bound.
	got, secret, err := st.Resolve(ctx, in.ID)
	if err != nil || got.OrgID != org || string(secret) != "sealed" {
		t.Fatalf("Resolve = %+v, %q, %v", got, secret, err)
	}
	if _, _, err := st.Resolve(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	dup := storedTrigger(org, tmpl, triggers.DefaultRateLimit)
	dup.Name = in.Name
	if _, err := st.Create(scoped, dup, []byte("x")); !errors.Is(err, triggers.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	// Deleting the template cascades.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM job_templates WHERE id = $1`, tmpl); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(scoped, org, in.ID); !errors.Is(err, triggers.ErrNotFound) {
		t.Fatalf("after template delete: %v", err)
	}
}

// TestRateLimitIsSharedAcrossManagers: two stores ("managers") over one
// database, 20 concurrent takes against a 10/min bucket: exactly 10 pass.
func TestRateLimitIsSharedAcrossManagers(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-bucket")
	rl := triggers.RateLimit{Count: 10, Per: "1m"}
	a, b := triggers.NewSQLStore(d), triggers.NewSQLStore(d)
	in := storedTrigger(org, tmpl, rl)
	if _, err := a.Create(db.WithOrgScope(ctx, org), in, []byte("s")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var mu sync.Mutex
	passed, limited := 0, 0
	var retry time.Duration
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		st := a
		if i%2 == 1 {
			st = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, wait, err := st.TakeToken(db.WithOrgScope(ctx, org), org, in.ID, rl, now)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				passed++
			} else {
				limited++
				retry = wait
			}
		}()
	}
	wg.Wait()
	if passed != 10 || limited != 10 {
		t.Fatalf("passed %d, limited %d; want 10/10", passed, limited)
	}
	if retry < time.Second || retry > 7*time.Second {
		t.Fatalf("retry after = %v, want about 6s (one token per 6s)", retry)
	}
	// A minute later the bucket is full again.
	if ok, _, _ := a.TakeToken(db.WithOrgScope(ctx, org), org, in.ID, rl, now.Add(time.Minute)); !ok {
		t.Fatal("the bucket did not refill")
	}
}

// TestThrottledDeliveryIsRaceFree covers Task 9 review Minor 1: the
// original NOT EXISTS dedup read a statement snapshot, so N concurrent
// rejected deliveries in the same trigger, outcome and minute could all
// see no row and all insert — exactly the flood case the throttle exists
// for. 20 concurrent rejections in the same minute must leave exactly one
// row.
func TestThrottledDeliveryIsRaceFree(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-throttle-race")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit)
	if _, err := st.Create(scoped, in, []byte("s")); err != nil {
		t.Fatal(err)
	}
	// A fixed timestamp safely mid-minute, not time.Now(): when the wall
	// clock happens to be in second 58 or 59, the 20 offsets below (up to
	// +19ms) can straddle a calendar-minute boundary and legitimately
	// produce 2 rows instead of 1 (Task 10 review, Important 2).
	at := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- st.RecordDelivery(scoped, triggers.Delivery{
				ID: uuid.Must(uuid.NewV7()), OrgID: org, TriggerID: in.ID,
				At: at.Add(time.Duration(i) * time.Millisecond), Outcome: triggers.OutcomeRejectedAuth,
			}, true)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM trigger_deliveries WHERE trigger_id = $1 AND outcome = 'rejected_auth'`, in.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("20 concurrent throttled deliveries produced %d rows, want exactly 1", count)
	}
}

// TestThrottledDuplicateSkipsTrim covers Task 9 review Minor 2: a
// suppressed (deduplicated) insert must not run the 500-row trim — there
// is nothing new to trim for, and running it anyway costs an extra
// statement on every flooded rejection.
func TestThrottledDuplicateSkipsTrim(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-skip-trim")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit)
	if _, err := st.Create(scoped, in, []byte("s")); err != nil {
		t.Fatal(err)
	}
	// A fixed timestamp safely mid-minute, not time.Now(): the "duplicate"
	// below adds 30s, which must land in the same calendar minute as at
	// (trigger_delivery_minute truncates to a UTC minute boundary) on
	// every run, not just when the test happens not to straddle one.
	at := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	if err := st.RecordDelivery(scoped, triggers.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: org, TriggerID: in.ID, At: at, Outcome: triggers.OutcomeRejectedAuth}, true); err != nil {
		t.Fatal(err)
	}
	// Push the trigger's row count well past 500 directly, bypassing
	// RecordDelivery's own trim, so a trim that ran anyway would be
	// observable.
	for i := 0; i < 600; i++ {
		if _, err := d.Pool().Exec(ctx, `INSERT INTO trigger_deliveries (id, org_id, trigger_id, at, outcome) VALUES ($1,$2,$3,$4,'triggered')`,
			uuid.Must(uuid.NewV7()), org, in.ID, at.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM trigger_deliveries WHERE trigger_id = $1`, in.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// A duplicate rejection in the same minute is deduplicated (0 rows
	// inserted); the trim must not run for it.
	if err := st.RecordDelivery(scoped, triggers.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: org, TriggerID: in.ID, At: at.Add(30 * time.Second), Outcome: triggers.OutcomeRejectedAuth}, true); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM trigger_deliveries WHERE trigger_id = $1`, in.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("count changed from %d to %d; a deduplicated insert must skip the trim", before, after)
	}
}

func TestDeliveriesAreThrottledAndTrimmed(t *testing.T) {
	d := testdb.Open(t)
	org, tmpl := seedTemplate(t, d, "trig-deliveries")
	st := triggers.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	in := storedTrigger(org, tmpl, triggers.DefaultRateLimit)
	if _, err := st.Create(scoped, in, []byte("s")); err != nil {
		t.Fatal(err)
	}
	// A fixed timestamp safely mid-minute, not time.Now(): the three
	// rejections below (up to +2s) can otherwise straddle a calendar-minute
	// boundary and legitimately produce 2 rows instead of 1 (Task 10
	// review, Important 2).
	at := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := st.RecordDelivery(scoped, triggers.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: org, TriggerID: in.ID, At: at.Add(time.Duration(i) * time.Second), SourceIP: "203.0.113.7", Outcome: triggers.OutcomeRejectedAuth}, true); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.Deliveries(scoped, org, in.ID, 50)
	if err != nil || len(list) != 1 {
		t.Fatalf("throttled rejections = %d, %v; want 1 row per outcome and minute", len(list), err)
	}
	for i := 0; i < 505; i++ {
		if err := st.RecordDelivery(scoped, triggers.Delivery{ID: uuid.Must(uuid.NewV7()), OrgID: org, TriggerID: in.ID, At: at.Add(time.Duration(i) * time.Millisecond), Outcome: triggers.OutcomeTriggered}, false); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM trigger_deliveries WHERE trigger_id = $1`, in.ID).Scan(&count); err != nil || count != 500 {
		t.Fatalf("rows after trim = %d, %v", count, err)
	}
}
