// The run's terminal stats: ansible's recap (the playbook_on_stats event's
// event_data) mapped onto the claim's stats contract (internal/jobs,
// Task 5): failed_hosts / unreachable_hosts — which RetryFailed reads, and
// which carry the claim's host UUIDs where the connection map names one —
// plus the ok/changed/skipped host lists ansible's recap carries.
package runner

import (
	"encoding/json"
	"slices"
)

// Stats keys of the claim's contract. failed_hosts and unreachable_hosts
// are arrays of host UUID strings (the worker maps ansible's host names
// through the claim's connections); hosts the claim does not name (an
// implicit localhost) stay bare names — RetryFailed intersects against the
// job's own targets, so an unknown name can never widen a retry.
const (
	statsFailedHosts      = "failed_hosts"
	statsUnreachableHosts = "unreachable_hosts"
	statsOKHosts          = "ok_hosts"
	statsChangedHosts     = "changed_hosts"
	statsSkippedHosts     = "skipped_hosts"
)

// buildStats maps the recap onto the claim's stats contract. connections is
// the claim's hostname → connection JSON map (the source of host uuids).
func buildStats(stats map[string]hostStat, connections map[string]string) map[string]any {
	ids := map[string]string{}
	for name, raw := range connections {
		var conn connection
		if err := json.Unmarshal([]byte(raw), &conn); err == nil && conn.HostID != "" {
			ids[name] = conn.HostID
		}
	}
	idOf := func(host string) string {
		if id, ok := ids[host]; ok {
			return id
		}
		return host
	}

	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	slices.Sort(names)

	var failed, unreachable, ok, changed, skipped []string
	for _, name := range names {
		s := stats[name]
		// A host can fail one task and lose connection on the next; the
		// failed and unreachable lists are independent, a host lands in
		// each list its counters name.
		if s.Failed > 0 {
			failed = append(failed, idOf(name))
		}
		if s.Unreachable > 0 {
			unreachable = append(unreachable, idOf(name))
		}
		// The ok list carries every host the recap counted, failed or not.
		if s.Ok > 0 {
			ok = append(ok, idOf(name))
		}
		if s.Changed > 0 {
			changed = append(changed, idOf(name))
		}
		if s.Skipped > 0 {
			skipped = append(skipped, idOf(name))
		}
	}
	return map[string]any{
		statsFailedHosts:      failed,
		statsUnreachableHosts: unreachable,
		statsOKHosts:          ok,
		statsChangedHosts:     changed,
		statsSkippedHosts:     skipped,
	}
}
