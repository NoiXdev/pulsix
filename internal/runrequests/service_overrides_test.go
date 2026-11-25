package runrequests_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

func boolPtr(v bool) *bool { return &v }

func (w *wiring) createWith(in runrequests.CreateInput) (runrequests.Request, error) {
	if in.Kind == "" {
		in.Kind = runrequests.KindRun
	}
	if in.TemplateID == uuid.Nil && in.Kind == runrequests.KindRun {
		in.TemplateID = uuid.Must(uuid.NewV7())
	}
	if in.Reason == "" {
		in.Reason = "roll out"
	}
	in.RequestedByUser = uuid.Must(uuid.NewV7())
	return w.svc.Create(context.Background(), w.org, in)
}

func TestCreateStoresNormalisedOverrides(t *testing.T) {
	w := newWiring()
	h1, h2 := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	limit := " web-1 "
	r, err := w.createWith(runrequests.CreateInput{Overrides: runrequests.OverridesInput{
		CheckMode: true, ExtraVars: map[string]any{"version": "1.2"}, HostIDs: []uuid.UUID{h2, h1, h2}, Limit: &limit,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ov := r.Overrides
	// diff defaults to on with check mode; hosts are deduplicated and
	// sorted; the limit is trimmed.
	if ov == nil || !ov.Check || !ov.Diff || ov.ExtraVars["version"] != "1.2" || ov.Limit == nil || *ov.Limit != "web-1" {
		t.Fatalf("overrides = %+v", ov)
	}
	want := []uuid.UUID{h1, h2}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if !slices.Equal(ov.HostIDs, want) {
		t.Fatalf("host ids = %v, want %v", ov.HostIDs, want)
	}
	if w.val.overrides == nil || !w.val.overrides.Check {
		t.Fatalf("validator saw %+v, want the overrides", w.val.overrides)
	}
	if stored := w.store.rows[r.ID]; stored.Overrides == nil || !stored.Overrides.Check {
		t.Fatalf("stored overrides = %+v", stored.Overrides)
	}

	// A dry run without diff, and a request without overrides at all.
	r, err = w.createWith(runrequests.CreateInput{Overrides: runrequests.OverridesInput{CheckMode: true, Diff: boolPtr(false)}})
	if err != nil || r.Overrides == nil || !r.Overrides.Check || r.Overrides.Diff {
		t.Fatalf("dry run without diff = %+v %v", r.Overrides, err)
	}
	empty := ""
	r, err = w.createWith(runrequests.CreateInput{Overrides: runrequests.OverridesInput{ExtraVars: map[string]any{}, Limit: &empty}})
	if err != nil || r.Overrides != nil {
		t.Fatalf("no overrides = %+v %v, want nil", r.Overrides, err)
	}
}

func TestCreateRefusesInvalidOverrides(t *testing.T) {
	host := uuid.Must(uuid.NewV7())
	job := uuid.Must(uuid.NewV7())
	many := make([]uuid.UUID, runrequests.MaxHostIDs+1)
	for i := range many {
		many[i] = uuid.Must(uuid.NewV7())
	}
	for _, tc := range []struct {
		name string
		in   runrequests.CreateInput
		msg  string
	}{
		{"diff without check mode", runrequests.CreateInput{Overrides: runrequests.OverridesInput{Diff: boolPtr(true)}}, "diff"},
		{"host_id and host_ids", runrequests.CreateInput{HostID: &host, Overrides: runrequests.OverridesInput{HostIDs: []uuid.UUID{host}}}, "host_ids"},
		{"too many hosts", runrequests.CreateInput{Overrides: runrequests.OverridesInput{HostIDs: many}}, "500"},
		{"retry with overrides", runrequests.CreateInput{Kind: runrequests.KindRetryFailed, JobID: &job, Overrides: runrequests.OverridesInput{CheckMode: true}}, "retry"},
		{"expiry too short", runrequests.CreateInput{ExpiresIn: 4 * time.Minute}, "5"},
		{"expiry too long", runrequests.CreateInput{ExpiresIn: 1441 * time.Minute}, "1440"},
		{"expiry negative", runrequests.CreateInput{ExpiresIn: -time.Minute}, "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWiring()
			_, err := w.createWith(tc.in)
			if !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("create = %v, want ErrInvalidInput naming %q", err, tc.msg)
			}
			if len(w.store.rows) != 0 {
				t.Fatal("an invalid request was stored")
			}
		})
	}
}

func TestCreateTakesAnExpiry(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
	}{{0, time.Hour}, {5 * time.Minute, 5 * time.Minute}, {1440 * time.Minute, 24 * time.Hour}} {
		w := newWiring()
		r, err := w.createWith(runrequests.CreateInput{ExpiresIn: tc.in})
		if err != nil || !r.ExpiresAt.Equal(w.now.Add(tc.want)) {
			t.Fatalf("expires_in %s = %v %v, want now+%s", tc.in, r.ExpiresAt, err, tc.want)
		}
	}
}

func TestApproveEnqueuesTheRequestWithItsOverrides(t *testing.T) {
	w := newWiring()
	host := uuid.Must(uuid.NewV7())
	r, err := w.createWith(runrequests.CreateInput{Overrides: runrequests.OverridesInput{CheckMode: true, HostIDs: []uuid.UUID{host}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, fp, err := w.svc.Get(context.Background(), w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(context.Background(), w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatal(err)
	}
	got := w.enq.request
	if got.ID != r.ID || got.Overrides == nil || !got.Overrides.Check || !slices.Equal(got.Overrides.HostIDs, []uuid.UUID{host}) {
		t.Fatalf("enqueued %+v, want the request with its overrides", got)
	}
}
