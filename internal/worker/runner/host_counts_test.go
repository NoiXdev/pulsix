package runner

import "testing"

// Backlog 3.129: two inventory names that map to one host id add up in
// host_counts instead of the later one overwriting the first.
func TestBuildStatsHostCountsSumForOneHostID(t *testing.T) {
	const id = "0190a000-0000-7000-8000-000000000001"
	conn := `{"host_id":"` + id + `"}`
	out := buildStats(map[string]hostStat{
		"web-a": {Ok: 2, Changed: 1, Skipped: 1},
		"web-b": {Ok: 3, Failed: 1, Unreachable: 2},
	}, map[string]string{"web-a": conn, "web-b": conn})
	counts := out[statsHostCounts].(map[string]hostCounts)
	want := hostCounts{Ok: 5, Changed: 1, Skipped: 1, Failed: 1, Unreachable: 2}
	if len(counts) != 1 || counts[id] != want {
		t.Fatalf("host_counts = %+v, want {%s: %+v}", counts, id, want)
	}
}

// The per-status host lists name a host once even when two inventory names
// map to it.
func TestBuildStatsHostListsHaveNoDuplicateIDs(t *testing.T) {
	const id = "0190a000-0000-7000-8000-000000000001"
	conn := `{"host_id":"` + id + `"}`
	out := buildStats(map[string]hostStat{
		"web-a": {Ok: 1, Changed: 1, Skipped: 1, Failed: 1, Unreachable: 1},
		"web-b": {Ok: 1, Changed: 1, Skipped: 1, Failed: 1, Unreachable: 1},
	}, map[string]string{"web-a": conn, "web-b": conn})
	for _, key := range []string{statsFailedHosts, statsUnreachableHosts, statsOKHosts, statsChangedHosts, statsSkippedHosts} {
		if got := out[key].([]string); len(got) != 1 || got[0] != id {
			t.Errorf("%s = %v, want [%s]", key, got, id)
		}
	}
}
