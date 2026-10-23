package notify_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/notify"
)

func seedOrg(t *testing.T, d *db.DB, slug string) uuid.UUID {
	t.Helper()
	o, err := gen.New(d.Pool()).CreateOrganization(ctx, gen.CreateOrganizationParams{ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID
}

func TestChannelStoreRoundTripAndIsolation(t *testing.T) {
	d := testdb.Open(t)
	orgA, orgB := seedOrg(t, d, "notify-a"), seedOrg(t, d, "notify-b")
	st := notify.NewSQLStore(d)
	scopedA, scopedB := db.WithOrgScope(ctx, orgA), db.WithOrgScope(ctx, orgB)
	cfg, _ := json.Marshal(notify.EmailConfig{Recipients: []string{"ops@example.test"}})
	c, err := st.CreateChannel(scopedA, notify.Channel{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, Kind: notify.KindEmail, Name: "ops", Enabled: true, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision != 1 || c.Kind != notify.KindEmail || c.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", c)
	}
	if _, err := st.CreateChannel(scopedA, notify.Channel{ID: uuid.Must(uuid.NewV7()), OrgID: orgA, Kind: notify.KindEmail, Name: "ops", Config: cfg}); !errors.Is(err, notify.ErrNameTaken) {
		t.Fatalf("duplicate name = %v", err)
	}
	// Another organization's scope sees nothing, even asking with orgA's id.
	if _, err := st.GetChannel(scopedB, orgA, c.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("cross-org get = %v", err)
	}
	if list, err := st.ListChannels(scopedB, orgA); err != nil || len(list) != 0 {
		t.Fatalf("cross-org list = %v, %v", list, err)
	}
	c.Name, c.Enabled = "ops mail", false
	up, err := st.UpdateChannel(scopedA, c, 1)
	if err != nil || up.Revision != 2 || up.Name != "ops mail" || up.Enabled {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := st.UpdateChannel(scopedA, c, 1); !errors.Is(err, notify.ErrRevisionMismatch) {
		t.Fatalf("stale update = %v", err)
	}
	c.ID = uuid.Must(uuid.NewV7())
	if _, err := st.UpdateChannel(scopedA, c, 1); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("unknown update = %v", err)
	}
	if err := st.DeleteChannel(scopedA, orgA, up.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteChannel(scopedA, orgA, up.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// TestOrgScheduleCategoriesGroupsRowsBySchedule (2.73): one query for the
// whole org must return every schedule's rows grouped correctly, so the
// service layer's union lands on the right schedule rather than mixing two
// schedules' categories together. newOutboxWorld's schedule has failure and
// change on one channel and skipped on two others (one disabled) — this
// store method does not care about a channel's enabled state, the same as
// ScheduleRules it is modeled on: it is reporting what is configured, not
// what would actually fire.
func TestOrgScheduleCategoriesGroupsRowsBySchedule(t *testing.T) {
	w := newOutboxWorld(t, "notify-categories")
	st := notify.NewSQLStore(w.d)
	scoped := db.WithOrgScope(ctx, w.org)

	// A second, ruleless schedule in the same org must not appear at all.
	q := gen.New(w.d.Pool())
	inline := "- hosts: all\n  tasks: []\n"
	pb, err := q.CreatePlaybook(ctx, gen.CreatePlaybookParams{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Inline: &inline, Source: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := q.InsertTemplate(ctx, gen.InsertTemplateParams{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, Name: "no-notify", PlaybookID: db.NullUUID(&pb.ID), Scope: []byte(`{}`), ExtraVars: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := q.CreateSchedule(ctx, gen.CreateScheduleParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: w.org, TemplateID: tmpl.ID, Name: "Bare", Mode: "cron", Cron: "0 4 * * *",
		Timezone: "UTC", Guards: []byte(`{"overlap":"skip","catch_up":{"enabled":false,"window":"1h"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.OrgScheduleCategories(scoped, w.org)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := got[w.schedule]
	if !ok {
		t.Fatalf("schedule %s missing from %+v", w.schedule, got)
	}
	seen := map[string]bool{}
	for _, events := range rows {
		for _, e := range events {
			seen[e] = true
		}
	}
	if len(seen) != 3 || !seen["failure"] || !seen["change"] || !seen["skipped"] {
		t.Fatalf("categories seen for %s = %+v, want failure, change, skipped", w.schedule, seen)
	}
	if _, ok := got[bare.ID]; ok {
		t.Fatalf("a schedule with no notify rules must not appear: %+v", got)
	}
}

// TestResendDeliveryOnlyMatchesAFailedRow is backlog 2.75's store half.
// The status predicate lives in the UPDATE rather than in a check
// before it, so two operators pressing resend at the same moment — or
// one racing the dispatcher, which may have claimed the row meanwhile —
// cannot both reset it. And an e-mail row's pending_recipients must
// survive: after a partial send those are the addresses still owed the
// message (backlog 2.74), and a resend owes exactly them, not the whole
// list again.
func TestResendDeliveryOnlyMatchesAFailedRow(t *testing.T) {
	w := newOutboxWorld(t, "delivery-resend")
	st := notify.NewSQLStore(w.d)
	scoped := db.WithOrgScope(ctx, w.org)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)

	insert := func(status notify.DeliveryStatus, pending []string) notify.Delivery {
		t.Helper()
		d := notify.Delivery{
			ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ChannelID: w.chA.ID,
			EventKey: "k-" + string(status) + uuid.Must(uuid.NewV7()).String(), EventKind: notify.EventTest,
			Payload: []byte(`{"kind":"test"}`), Status: status, Attempts: 4,
			NextAttemptAt: now, LastError: "the endpoint answered 500", CreatedAt: now,
		}
		if err := st.InsertDelivery(scoped, d); err != nil {
			t.Fatal(err)
		}
		// pending_recipients only ever arrives through the retry path
		// (backlog 2.74) — InsertDelivery leaves it NULL, which means
		// "the channel's own list" — so the test writes it the way a
		// partial send would rather than through an insert that never
		// sets it in production.
		if pending != nil {
			if _, err := w.d.Pool().Exec(ctx, `UPDATE notification_deliveries SET pending_recipients = $2 WHERE id = $1`, d.ID, pending); err != nil {
				t.Fatal(err)
			}
			d.PendingRecipients = pending
		}
		return d
	}

	failed := insert(notify.DeliveryFailed, []string{"dba@example.test"})
	got, err := st.ResendDelivery(scoped, w.org, w.chA.ID, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != notify.DeliveryPending || got.Attempts != 0 || got.LastError != "" || got.SentAt != nil {
		t.Fatalf("resent = %+v, want pending with a fresh budget", got)
	}
	if len(got.PendingRecipients) != 1 || got.PendingRecipients[0] != "dba@example.test" {
		t.Fatalf("pending recipients = %v, want the addresses still owed the message", got.PendingRecipients)
	}

	// The same row again: it is pending now, so the predicate matches
	// nothing — this is what stops a second press from resetting a row
	// the dispatcher is already working on.
	if _, err := st.ResendDelivery(scoped, w.org, w.chA.ID, failed.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("resending a pending row = %v, want ErrNotFound for the caller to interpret", err)
	}
	sent := insert(notify.DeliverySent, nil)
	if _, err := st.ResendDelivery(scoped, w.org, w.chA.ID, sent.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("resending a sent row = %v", err)
	}
	// A delivery of another channel is not this channel's to resend.
	if _, err := st.ResendDelivery(scoped, w.org, w.chB.ID, failed.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("cross-channel resend = %v", err)
	}
	if _, err := st.GetDelivery(scoped, w.org, w.chB.ID, failed.ID); !errors.Is(err, notify.ErrNotFound) {
		t.Fatalf("cross-channel read = %v", err)
	}
}
