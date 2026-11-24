package runrequests_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/runrequests"
)

// TestPictureTargetsAreResolvedUnderTheLimit: a run's targets are the
// scope under the template's own limit (LimitedRunTargets), what creation
// and approval check, not the unlimited set the maintenance guard asks for;
// a scope the limit empties shows no targets.
func TestPictureTargetsAreResolvedUnderTheLimit(t *testing.T) {
	w := newWorld()
	w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x})
	if w.jobs.limitedCalls != 1 {
		t.Fatalf("limited target questions = %d, want 1", w.jobs.limitedCalls)
	}

	w.jobs.targets = []uuid.UUID{}
	if p := w.picture(t, runrequests.Request{Kind: runrequests.KindRun, TemplateID: w.x}); len(p.Targets) != 0 {
		t.Fatalf("targets of an emptied scope = %+v, want none", p.Targets)
	}
}
