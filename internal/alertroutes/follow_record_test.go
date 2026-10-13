// The ledger-write predicate, tested from inside the package: it is three
// lines with no database in reach, and the one property that matters about
// it is a negative — a decision of 'sent' is NEVER skipped — which an
// end-to-end test cannot demonstrate because the catch-up pass has no way
// to produce the input that would break it.
package alertroutes

import "testing"

func TestWriteNeededNeverSkipsSent(t *testing.T) {
	tests := []struct {
		name  string
		st    LedgerState
		found LedgerState
		want  bool
	}{
		{"forward pass, no row yet", StateSent, "", true},
		{"still suppressed", StateSuppressed, StateSuppressed, false},
		{"still deferred", StateDeferred, StateDeferred, false},
		{"the window shut over a suppressed event", StateDeferred, StateSuppressed, true},
		{"maintenance began over a deferred event", StateSuppressed, StateDeferred, true},
		{"the route was deleted under a held event", StateUnrouted, StateDeferred, true},
		// The guard itself. The catch-up pass reads only held states, so
		// it cannot hand 'sent' back today and this row is unreachable
		// through the follower — which is exactly why it is here. The
		// write and the enqueue share one transaction; an enqueue with no
		// ledger write is 'sent' without the delivery that reports it,
		// the one failure the whole feature exists to prevent, and
		// leaving its absence to rest on a WHERE clause in another file
		// is leaving it to the next person to widen that clause.
		{"sent must always be written, whatever the ledger says", StateSent, StateSent, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := writeNeeded(tt.st, tt.found); got != tt.want {
				t.Errorf("writeNeeded(%q, %q) = %v, want %v", tt.st, tt.found, got, tt.want)
			}
		})
	}
}
