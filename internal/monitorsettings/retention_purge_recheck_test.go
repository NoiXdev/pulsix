package monitorsettings_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/monitorsettings"
)

// TestPurgeRechecksTheChoiceBeforeDeleting: the list is read once per
// round, so right before an org is purged its current choice is read
// again. Raised to the maximum or above, cleared, or raised to a longer
// shorter choice (the cutoff moved earlier): nothing is deleted this
// round. Lowered: the round still deletes only by the listed, longer
// retention (the next round uses the new one).
func TestPurgeRechecksTheChoiceBeforeDeleting(t *testing.T) {
	cases := []struct {
		name          string
		listed, now   int
		wantDeleted   int64
		wantCutoffAge int // days; the newest range end must be this old
	}{
		{"raised above the maximum", 30, 365, 0, 0},
		{"cleared", 30, 0, 0, 0},
		{"raised but still shorter", 30, 90, 0, 0},
		{"unchanged", 30, 30, 3, 30},
		{"lowered", 90, 30, 2, 90},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clock := &fakeClock{now: purgeNow}
			store := newFakePurgeStore(clock)
			org := uuid.Must(uuid.NewV7())
			store.orgs = []monitorsettings.OrgRetention{{OrgID: org, Days: c.listed}}
			store.choices = map[uuid.UUID]int{org: c.now}
			store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(120), dayRange(95), dayRange(40)}
			for _, d := range []int{120, 95, 40} {
				store.put(monitorsettings.TableCheckSamples, org, day(d), 1)
			}
			res := newTestPurger(store, nil, 180, nil).Round(ctx)
			if res.Samples != c.wantDeleted || res.Errors != 0 {
				t.Fatalf("result = %+v, want %d deleted, no errors", res, c.wantDeleted)
			}
			for _, call := range store.calls {
				if call.r.End.After(purgeNow.AddDate(0, 0, -c.wantCutoffAge)) {
					t.Fatalf("range %+v past the %d-day cutoff", call.r, c.wantCutoffAge)
				}
			}
		})
	}

	// A failing re-read is an org error; nothing is deleted for it.
	clock := &fakeClock{now: purgeNow}
	store := newFakePurgeStore(clock)
	store.orgs = []monitorsettings.OrgRetention{{OrgID: uuid.Must(uuid.NewV7()), Days: 30}}
	store.choiceErr = errors.New("db down")
	store.ranges[monitorsettings.TableCheckSamples] = []monitorsettings.TimeRange{dayRange(40)}
	if res := newTestPurger(store, nil, 90, nil).Round(ctx); res.Errors != 1 || len(store.calls) != 0 {
		t.Fatalf("result = %+v with %d calls, want 1 error and no deletes", res, len(store.calls))
	}
}
