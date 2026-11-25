package runrequests_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestFingerprintCoversTheEnrollment (spec 2026-11-25 B): the hosts a run
// enrols agents on are in the fingerprint, in any order; a picture without
// the section keeps its fingerprint (the golden test).
func TestFingerprintCoversTheEnrollment(t *testing.T) {
	base := goldenPicture()
	base.AgentEnrollment = &runrequests.PictureAgentEnrollment{Hosts: []string{"web-2", "web-1"}, HostIDs: []string{"h2", "h1"}}
	fp := runrequests.Fingerprint(goldenRequest, base)
	if fp == goldenFingerprint {
		t.Fatal("the enrollment does not change the fingerprint")
	}
	reordered := base
	reordered.AgentEnrollment = &runrequests.PictureAgentEnrollment{Hosts: []string{"web-1", "web-2"}, HostIDs: []string{"h1", "h2"}}
	if runrequests.Fingerprint(goldenRequest, reordered) != fp {
		t.Fatal("the order of the enrolled hosts changes the fingerprint")
	}
	for name, e := range map[string]runrequests.PictureAgentEnrollment{
		"a host fewer": {Hosts: []string{"web-1"}, HostIDs: []string{"h1"}},
		"none":         {Hosts: []string{}, HostIDs: []string{}},
		"dry run":      {Hosts: []string{}, HostIDs: []string{}, NoneReason: runrequests.EnrollNoneDryRun},
	} {
		p := base
		p.AgentEnrollment = &e
		if runrequests.Fingerprint(goldenRequest, p) == fp {
			t.Errorf("%s does not change the fingerprint", name)
		}
	}
}
