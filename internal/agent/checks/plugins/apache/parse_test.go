package apache

import "testing"

func TestParseStatus(t *testing.T) {
	got, err := parseStatus([]byte(eventPage(1234, 56, 3, 72)))
	if err != nil {
		t.Fatal(err)
	}
	// The first BusyWorkers line wins over a later duplicate.
	want := status{busy: 3, idle: 72, accesses: 1234, kbytes: 56, extended: true}
	if got != want {
		t.Fatalf("parseStatus = %+v, want %+v", got, want)
	}
	got, err = parseStatus([]byte(preforkPage(1, 4)))
	if err != nil || got != (status{busy: 1, idle: 4}) {
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
		if err != nil || got != (status{busy: 1, idle: 4}) {
			t.Fatalf("parseStatus(%q) = %+v, %v", body, got, err)
		}
	}
}
