package apache

import (
	"strings"
	"testing"
)

func TestParseStatus(t *testing.T) {
	got, err := parseStatus([]byte(eventPage(1234, 56, 3, 72)))
	if err != nil {
		t.Fatal(err)
	}
	// The first BusyWorkers line wins over a later duplicate.
	want := status{busy: 3, idle: 72, accesses: 1234, kbytes: 56, extended: true, slots: 22}
	if got != want {
		t.Fatalf("parseStatus = %+v, want %+v", got, want)
	}
	got, err = parseStatus([]byte(preforkPage(1, 4)))
	if err != nil || got != (status{busy: 1, idle: 4, slots: 6}) {
		t.Fatalf("parseStatus(prefork) = %+v, %v", got, err)
	}
}

// Only one of Total Accesses / Total kBytes counts as ExtendedStatus Off: a
// rate needs both baselines.
func TestParseStatusOneCounterOnly(t *testing.T) {
	for _, body := range []string{
		"BusyWorkers: 1\nIdleWorkers: 4\nTotal Accesses: 7\n",
		"BusyWorkers: 1\nIdleWorkers: 4\nTotal kBytes: 7\n",
	} {
		got, err := parseStatus([]byte(body))
		if err != nil || got != (status{busy: 1, idle: 4, slotsReason: reasonNoScoreboard}) {
			t.Fatalf("parseStatus(%q) = %+v, %v", body, got, err)
		}
	}
}

// The Scoreboard line has one character per worker slot; in normal running
// mod_status prints exactly the server's worker limit of them (backlog
// 3.11b, R1), after a graceful restart possibly more. A
// line that is missing or not a scoreboard costs only the two metrics built
// on it, never the page.
func TestParseScoreboard(t *testing.T) {
	const head = "BusyWorkers: 1\nIdleWorkers: 4\n"
	tests := []struct {
		name   string
		body   string
		slots  uint64
		reason string
	}{
		{"every state", head + "Scoreboard: _SRWKDCLGI.\n", 11, ""},
		// A graceful restart from MaxRequestWorkers 150 to 50 under load
		// leaves the old slots dead before the new ones (backlog 3.11b
		// review): the line is longer than the limit, read as it is.
		{"longer than the limit", head + "Scoreboard: " + strings.Repeat(".", 50) + "W" + strings.Repeat("_", 49) + "\n", 100, ""},
		{"no trailing newline", head + "Scoreboard: W____", 5, ""},
		{"CRLF", head + "Scoreboard: W____\r\n", 5, ""},
		{"first line wins", head + "Scoreboard: W_\nScoreboard: W____\n", 2, ""},
		{"absent", head, 0, reasonNoScoreboard},
		{"empty", head + "Scoreboard: \n", 0, "unexpected response format"},
		{"foreign character", head + "Scoreboard: W_x_\n", 0, "unexpected response format"},
		{"inner blank", head + "Scoreboard: W_ __\n", 0, "unexpected response format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStatus([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if got.slots != tt.slots || got.slotsReason != tt.reason || got.busy != 1 || got.idle != 4 {
				t.Fatalf("parseStatus = %+v, want slots %d reason %q", got, tt.slots, tt.reason)
			}
		})
	}
}
