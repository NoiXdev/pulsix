package api

import "testing"

// TestSortFleetHosts pins sortFleetHosts' own contract directly, bypassing
// the whole HTTP route and its database-backed roster (backlog 3.34):
// worst state first, and — the half nothing before this test could ever
// exercise — alphabetical by name within a tied state. fleetRoster's own
// caller, hosts.Service.QueryHosts, always hands the roster in name order
// already, so every fixture built through the full route agreed on the
// tie-break whether or not sortFleetHosts carried one of its own; this
// table deliberately scrambles the input by name within each state so
// only an explicit secondary key — not the input's own pre-existing order
// — can produce the expected result.
func TestSortFleetHosts(t *testing.T) {
	crit, warn, unknown := "crit", "warn", "unknown"
	hosts := []fleetHostDTO{
		{Name: "zeta", State: &crit},
		{Name: "mike", State: &unknown},
		{Name: "bravo", State: nil}, // no data: ranks last of all
		{Name: "delta", State: &warn},
		{Name: "alpha", State: &crit},
		{Name: "omega", State: nil},
		{Name: "charlie", State: &warn},
		{Name: "hotel", State: &unknown},
	}
	sortFleetHosts(hosts)

	want := []string{
		"alpha", "zeta", // crit, alphabetical
		"charlie", "delta", // warn, alphabetical
		"hotel", "mike", // unknown, alphabetical
		"bravo", "omega", // no data, alphabetical, trailing every judged state
	}
	got := make([]string, len(hosts))
	for i, h := range hosts {
		got[i] = h.Name
	}
	if len(got) != len(want) {
		t.Fatalf("sortFleetHosts produced %d hosts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortFleetHosts = %v, want %v (first mismatch at %d: got %q, want %q)", got, want, i, got[i], want[i])
		}
	}
}

// TestSortFleetHostsIsStableUnderRepetition runs sortFleetHosts against
// several independently shuffled orderings of the same multiset and
// requires byte-identical output every time — a comparator that is not a
// genuine total order (inconsistent for some pair, or one that only
// happens to look right on a particular input permutation) is exactly
// what this is positioned to catch, where a single fixed-order table might
// pass by luck on the one permutation it happens to try.
func TestSortFleetHostsIsStableUnderRepetition(t *testing.T) {
	crit, warn := "crit", "warn"
	base := []fleetHostDTO{
		{Name: "alpha", State: &crit},
		{Name: "bravo", State: &crit},
		{Name: "charlie", State: &warn},
		{Name: "delta", State: &warn},
		{Name: "echo", State: nil},
		{Name: "foxtrot", State: nil},
	}
	want := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot"}

	// Every rotation of base is a distinct input permutation; sortFleetHosts
	// must land on the identical, fully-determined answer from each one.
	for rot := 0; rot < len(base); rot++ {
		shuffled := make([]fleetHostDTO, len(base))
		for i := range base {
			shuffled[i] = base[(i+rot)%len(base)]
		}
		sortFleetHosts(shuffled)
		got := make([]string, len(shuffled))
		for i, h := range shuffled {
			got[i] = h.Name
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("rotation %d: sortFleetHosts = %v, want %v", rot, got, want)
			}
		}
	}
}
