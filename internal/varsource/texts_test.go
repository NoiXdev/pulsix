package varsource

import "testing"

// The playbook texts' stats keys and reasons are a wire contract between a
// worker and a manager of different versions: their values never change.
func TestPlaybookTextsWireValues(t *testing.T) {
	for got, want := range map[string]string{
		PlaybookTextsStatsKey:        "playbook_texts",
		PlaybookTextsLeftOutStatsKey: "playbook_texts_left_out",
		TextTooLarge:                 "too_large",
		TextBudget:                   "budget",
		TextNotUTF8:                  "not_utf8",
		TextUnreadable:               "unreadable",
	} {
		if got != want {
			t.Errorf("wire value %q, want %q", got, want)
		}
	}
	if MaxPlaybookTextsBytes != 4<<20 {
		t.Errorf("MaxPlaybookTextsBytes = %d, want 4 MiB", MaxPlaybookTextsBytes)
	}
}
