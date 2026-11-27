package alertroutes

import (
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/notify"
)

// TestCapItemsKeepsClosedItemsFirst pins backlog 4.43: when an alert batch
// is capped, its closed items are dropped last. A left-out opened item is a
// missed line in a message the count still summarises; a left-out closed
// item is a resolve a channel that follows alert state (an incident it
// opened earlier) never sees. The kept items keep their arrival order.
func TestCapItemsKeepsClosedItemsFirst(t *testing.T) {
	// items builds one item per letter: o = opened, c = closed, a =
	// acknowledged; Item carries the index so order is visible.
	items := func(kinds string) []notify.AlertItem {
		out := make([]notify.AlertItem, len(kinds))
		for i, k := range kinds {
			kind := map[rune]alerts.EventKind{'o': alerts.EventOpened, 'c': alerts.EventClosed, 'a': alerts.EventAcknowledged}[k]
			out[i] = notify.AlertItem{Event: string(kind), Item: string(rune('A' + i))}
		}
		return out
	}
	names := func(in []notify.AlertItem) string {
		s := ""
		for _, it := range in {
			s += it.Item
		}
		return s
	}

	tests := []struct {
		name  string
		kinds string
		max   int
		want  string
	}{
		{name: "under the cap keeps everything", kinds: "oco", max: 5, want: "ABC"},
		{name: "at the cap keeps everything", kinds: "ococ", max: 4, want: "ABCD"},
		{name: "no closed item keeps the first ones", kinds: "oaoo", max: 2, want: "AB"},
		{name: "closed items at the end are kept", kinds: "ooooc", max: 2, want: "AE"},
		{name: "every closed item before any other", kinds: "oocoocc", max: 3, want: "CFG"},
		{name: "more closed items than the cap keeps the first closed", kinds: "cocc", max: 2, want: "AC"},
		{name: "kept items stay in arrival order", kinds: "ocooc", max: 3, want: "ABE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := items(tc.kinds)
			orig := slices.Clone(in)
			if got := names(capItems(in, tc.max)); got != tc.want {
				t.Errorf("capItems(%q, %d) = %q, want %q", tc.kinds, tc.max, got, tc.want)
			}
			if names(in) != names(orig) {
				t.Errorf("capItems changed its input to %q", names(in))
			}
		})
	}
}
