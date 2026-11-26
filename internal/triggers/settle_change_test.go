package triggers_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/triggers"
)

// TestChangeSettle: a change that leaves the settle delay or the maximum
// wait out keeps it; a maximum wait of 0 is the default (ten times the
// delay); turning the delay off drops a maximum wait the change does not
// name.
func TestChangeSettle(t *testing.T) {
	cases := []struct {
		name            string
		settle          int
		maxWait         *int
		newSettle, newM *int
		wantSettle      int
		wantMaxWait     *int
	}{
		{"nothing given keeps both", 30, intp(600), nil, nil, 30, intp(600)},
		{"nothing given on an off trigger", 0, nil, nil, nil, 0, nil},
		{"a new delay keeps the maximum wait", 30, intp(600), intp(60), nil, 60, intp(600)},
		{"a new maximum wait keeps the delay", 30, intp(600), nil, intp(900), 30, intp(900)},
		{"a maximum wait of 0 is the default", 30, intp(600), nil, intp(0), 30, nil},
		{"turning the delay off drops the maximum wait", 30, intp(600), intp(0), nil, 0, nil},
		{"turning the delay off with a maximum wait keeps it (refused later)", 30, intp(600), intp(0), intp(60), 0, intp(60)},
		{"both given", 0, nil, intp(10), intp(20), 10, intp(20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := triggers.ChangeSettle(tc.settle, tc.maxWait, tc.newSettle, tc.newM)
			if s != tc.wantSettle || (m == nil) != (tc.wantMaxWait == nil) || (m != nil && *m != *tc.wantMaxWait) {
				t.Fatalf("ChangeSettle = %d %v, want %d %v", s, m, tc.wantSettle, tc.wantMaxWait)
			}
			if m != nil && m == tc.maxWait {
				t.Fatal("ChangeSettle returned the current maximum wait's pointer")
			}
		})
	}
}

// TestDeliverySupersededBy: the run that took the place of a superseded
// window's deliveries, read from the note the receive path appends.
func TestDeliverySupersededBy(t *testing.T) {
	run := uuid.Must(uuid.NewV7())
	cases := []struct {
		reason string
		want   *uuid.UUID
	}{
		{"", nil},
		{"superseded by a later delivery", nil},
		{"superseded by run " + run.String(), &run},
		{"mapped; superseded by run " + run.String(), &run},
		{"superseded by run not-a-uuid", nil},
	}
	for _, tc := range cases {
		got := triggers.Delivery{Outcome: triggers.OutcomeCollected, Reason: tc.reason}.SupersededBy()
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Fatalf("SupersededBy(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
	if got := (triggers.Delivery{Outcome: triggers.OutcomeTriggered, Reason: "superseded by run " + run.String()}).SupersededBy(); got != nil {
		t.Fatalf("a delivery that was not collected was superseded: %v", got)
	}
}
