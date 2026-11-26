package app

import (
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/sshconf"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// WireChangeReviews wires the pause after an assistant's change (spec
// 2026-11-25 assistant API gaps, C2 and decision D6): the template,
// repository, playbook and inventory services hand an assistant's
// run-relevant change to the reviewer inside the change's transaction,
// which pauses the schedules and disables the triggers it affects and opens
// their reviews (an inventory change: those whose runs reach the hosts it
// changes, decided by the scope reach on inv and hs); the schedule and trigger
// services close a review when a person resumes or enables its item. Every
// store involved is built on d, so all of it is one transaction.
func WireChangeReviews(d *db.DB, tmpl *templates.Service, rp *repos.Service, pb *playbooks.Service, inv *inventory.Service, hs *hosts.Service, ssh *sshconf.Service, sch *schedules.Service, trg *triggers.Service, rec *audit.Recorder) *runrequests.Reviewer {
	reviewer := runrequests.NewReviewer(runrequests.NewSQLStore(d),
		runrequests.ReviewSources{Templates: tmpl, Schedules: sch, Triggers: trg, History: audit.NewSQLStore(d), Reach: jobs.NewScopeReach(inv, inv, hs)}, rec,
		time.Now, func() uuid.UUID { return uuid.Must(uuid.NewV7()) })
	tmpl.SetChangeWatcher(reviewer)
	rp.SetChangeWatcher(reviewer)
	pb.SetChangeWatcher(reviewer)
	inv.SetChangeWatcher(reviewer)
	hs.SetChangeWatcher(reviewer)
	ssh.SetChangeWatcher(reviewer)
	sch.SetReviews(reviewer)
	trg.SetReviews(reviewer)
	return reviewer
}
