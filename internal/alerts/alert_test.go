package alerts

import (
	"testing"
)

func TestEventKindsMatchTheSchema(t *testing.T) {
	// The CHECK list in migration 00049 is the authority; this pins the
	// Go copy to it so the two cannot drift apart silently — the mistake
	// rules.StateRank's own comment records having made with
	// fleetStateRank.
	want := []EventKind{
		EventOpened, EventSeverityRaised, EventAcknowledged,
		EventAckVoided, EventSnoozed, EventUnsnoozed,
		EventClosed,
	}
	got := EventKinds()
	if len(got) != len(want) {
		t.Fatalf("EventKinds() = %d kinds, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("EventKinds()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, k := range want {
		if parsed, ok := ParseEventKind(string(k)); !ok || parsed != k {
			t.Errorf("ParseEventKind(%q) = %q, %v; want %q, true", k, parsed, ok, k)
		}
	}
	if _, ok := ParseEventKind("resolved"); ok {
		t.Error(`ParseEventKind("resolved") accepted a kind the schema does not have`)
	}
}
