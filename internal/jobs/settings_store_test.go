// The org-level job settings against the real store (backlog 2.44):
// default-false when no row is stored, a write-and-read round trip through
// the service, and the per-organization scoping the row-level security and
// the WHERE clause enforce together.
package jobs_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/jobs"
)

func TestSQLOrgJobSettingsRoundTripAndIsolation(t *testing.T) {
	w := newRotationStoreWiring(t, "org-job-settings")
	other := newRotationStoreWiring(t, "org-job-settings-other")

	st, err := w.svc.OrgJobSettings(w.scoped, w.org)
	if err != nil || st.JobsRequirePinnedHosts {
		t.Fatalf("before any write = %+v, %v; want the default false", st, err)
	}
	if err := w.svc.SetOrgJobSettings(w.scoped, w.org, jobs.OrgJobSettings{JobsRequirePinnedHosts: true}); err != nil {
		t.Fatalf("SetOrgJobSettings: %v", err)
	}
	st, err = w.svc.OrgJobSettings(w.scoped, w.org)
	if err != nil || !st.JobsRequirePinnedHosts {
		t.Fatalf("after the write = %+v, %v; want true", st, err)
	}
	// Another organization sees nothing: no leak through the shared table.
	st, err = other.svc.OrgJobSettings(other.scoped, other.org)
	if err != nil || st.JobsRequirePinnedHosts {
		t.Fatalf("the other org's read = %+v, %v; want the default", st, err)
	}
	// The store reports the row's presence precisely (no stored row vs. a
	// stored false), so the refusal's "unreadable" failure path stays
	// distinguishable from "off".
	store := jobs.NewSQLStore(w.d)
	require, ok, err := store.GetOrgJobSettings(w.scoped, w.org)
	if err != nil || !ok || !require.JobsRequirePinnedHosts {
		t.Fatalf("raw store read = %+v, ok=%v, %v; want the stored true", require, ok, err)
	}
	// An overwrite replaces the row instead of stacking.
	if err := w.svc.SetOrgJobSettings(w.scoped, w.org, jobs.OrgJobSettings{}); err != nil {
		t.Fatal(err)
	}
	if st, err = w.svc.OrgJobSettings(w.scoped, w.org); err != nil || st.JobsRequirePinnedHosts {
		t.Fatalf("after the overwrite = %+v, %v; want false", st, err)
	}
}
