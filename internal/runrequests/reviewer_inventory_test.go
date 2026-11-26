package runrequests_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/schedules"
	"github.com/noixdev/pulsix/internal/templates"
	"github.com/noixdev/pulsix/internal/triggers"
)

// List implements ReviewTemplates: the world's two templates, deploy
// scoped to group scopeA, with their limits.
func (w *reviewWorld) List(_ context.Context, _ uuid.UUID) ([]templates.Template, error) {
	return []templates.Template{
		{ID: w.tmplA, Name: "deploy", Limit: w.limits[w.tmplA], Scope: templates.Scope{GroupID: &w.scopeA}},
		{ID: w.tmplB, Name: "cleanup", Limit: w.limits[w.tmplB]},
	}, nil
}

// Overlap implements ReviewReach: the hosts each template reaches, as the
// test set them in overlap; changed records what was asked.
func (w *reviewWorld) Overlap(_ context.Context, _ uuid.UUID, changed []render.Scope, scopes map[uuid.UUID]render.Scope) (map[uuid.UUID][]string, error) {
	w.changed = append(w.changed, changed)
	out := map[uuid.UUID][]string{}
	for id := range scopes {
		if names := w.overlap[id]; len(names) > 0 {
			out[id] = names
		}
	}
	return out, nil
}

func (w *reviewWorld) groupChange(fields ...assistantchange.Field) assistantchange.Change {
	token := uuid.Must(uuid.NewV7())
	id := uuid.Must(uuid.NewV7())
	return assistantchange.Change{Subject: assistantchange.SubjectInventoryGroup, ID: id, Name: "web",
		UserID: uuid.Must(uuid.NewV7()), TokenID: &token, Fields: fields, Reach: &assistantchange.Reach{Group: &id}}
}

var portChange = assistantchange.Field{Name: "vars.port", Old: 80.0, New: 8080.0}

// TestAnAssistantsGroupChangePausesWhatRunsOnItsMembers (decision D6): an
// assistant's change of a group's values pauses every active schedule and
// disables every enabled trigger of the templates whose run reaches a
// member of the group, each with a review naming the change; the items of
// a template that reaches none are left alone.
func TestAnAssistantsGroupChangePausesWhatRunsOnItsMembers(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplA: {"web1"}}
	a := w.schedule("nightly", w.tmplA, false, "")
	b := w.schedule("cleanup", w.tmplB, false, "")
	hook := w.trigger("deploy-hook", &w.tmplA, nil, true)
	c := w.groupChange(portChange)
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	if len(w.changed) != 1 || len(w.changed[0]) != 1 || w.changed[0][0].GroupID == nil || *w.changed[0][0].GroupID != c.ID {
		t.Fatalf("asked the reach for %+v, want the group", w.changed)
	}
	if s := w.schedules[a]; !s.Paused || s.PausedReason != schedules.PausedForReview {
		t.Fatalf("schedule of the reaching template = %+v", s)
	}
	if w.schedules[b].Paused {
		t.Fatalf("schedule of the other template = %+v", w.schedules[b])
	}
	if tr := w.triggers[hook]; tr.Enabled || tr.DisabledReason != triggers.DisabledForReview {
		t.Fatalf("trigger = %+v", tr)
	}
	sr := w.reviewOf(a)
	if len(sr) != 1 || sr[0].TemplateID != w.tmplA || len(sr[0].Review.Changes) != 1 ||
		sr[0].Review.Changes[0].Subject != assistantchange.SubjectInventoryGroup || !strings.Contains(sr[0].Reason, "inventory group web") {
		t.Fatalf("review = %+v", sr)
	}
	if len(w.events) != 2 || !strings.Contains(w.events[0].Summary, "an assistant changed inventory group web (vars.port)") {
		t.Fatalf("events = %+v", w.events)
	}
}

// A membership change reaches only that host: the reach is asked for the
// host, and only the template whose run reaches it is paused.
func TestAnAssistantsMembershipChangePausesWhatRunsOnThatHost(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplB: {"db1"}}
	a := w.schedule("nightly", w.tmplA, false, "")
	b := w.schedule("cleanup", w.tmplB, false, "")
	host := uuid.Must(uuid.NewV7())
	c := w.groupChange(portChange)
	c.Subject, c.HostID, c.HostName = assistantchange.SubjectInventoryMembership, &host, "db1"
	c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{host}}
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	if len(w.changed) != 1 || len(w.changed[0]) != 1 || w.changed[0][0].GroupID != nil || len(w.changed[0][0].HostIDs) != 1 || w.changed[0][0].HostIDs[0] != host {
		t.Fatalf("asked the reach for %+v, want the host", w.changed)
	}
	if w.schedules[a].Paused || !w.schedules[b].Paused {
		t.Fatalf("schedules = %+v / %+v", w.schedules[a], w.schedules[b])
	}
	if sr := w.reviewOf(b); len(sr) != 1 || !strings.Contains(sr[0].Reason, "host db1 in inventory group web") {
		t.Fatalf("review = %+v", sr)
	}
	if !strings.Contains(w.events[0].Summary, "host db1 in inventory group web (vars.port)") {
		t.Fatalf("events = %+v", w.events)
	}
}

// The limit does not narrow the reach (final review I2): a limited run
// still renders every scoped host of its zones, and a play reads their
// values through hostvars, so a template whose limit excludes the reached
// hosts, a schedule with its own limit and a trigger are all paused.
func TestAnInventoryChangeIgnoresTheLimit(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplA: {"web1"}}
	w.limits = map[uuid.UUID]string{w.tmplA: "db*"}
	excluded := w.schedule("db only", w.tmplA, false, "")
	own := w.schedule("own limit elsewhere", w.tmplA, false, "")
	s := w.schedules[own]
	other := "db2"
	s.Overrides.Limit = &other
	w.schedules[own] = s
	hook := w.trigger("plain", &w.tmplA, nil, true)
	if err := w.reviewer.Changed(ctx, w.org, w.groupChange(portChange)); err != nil {
		t.Fatal(err)
	}
	if !w.schedules[excluded].Paused || !w.schedules[own].Paused || w.triggers[hook].Enabled {
		t.Fatalf("schedules %+v / %+v, trigger %+v: want all stopped", w.schedules[excluded], w.schedules[own], w.triggers[hook])
	}
}

// A template scoped to a group that held the reached hosts before the
// change (a host left it, a group moved away) is paused even though its
// scope no longer selects them (decision D7).
func TestAnInventoryChangeReachesTheFormerGroupsTemplates(t *testing.T) {
	w := newReviewWorld()
	a := w.schedule("nightly", w.tmplA, false, "")
	b := w.schedule("cleanup", w.tmplB, false, "")
	host := uuid.Must(uuid.NewV7())
	c := w.groupChange(assistantchange.Field{Name: "member", Old: "web1", New: nil})
	c.Subject, c.HostID, c.HostName = assistantchange.SubjectInventoryMembership, &host, "web1"
	c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{host}, FormerGroups: []uuid.UUID{w.scopeA}}
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	if !w.schedules[a].Paused || w.schedules[b].Paused {
		t.Fatalf("schedules %+v / %+v: want only the former group's template paused", w.schedules[a], w.schedules[b])
	}
}

// An organization without any schedule or template trigger is answered
// without reading what the change reaches (final review M2).
func TestAnInventoryChangeWithoutAutomationReadsNothing(t *testing.T) {
	w := newReviewWorld()
	w.reviews.noAutomation = true
	w.overlap = map[uuid.UUID][]string{w.tmplA: {"web1"}}
	if err := w.reviewer.Changed(ctx, w.org, w.groupChange(portChange)); err != nil {
		t.Fatal(err)
	}
	if len(w.changed) != 0 || len(w.reviews.rows) != 0 {
		t.Fatalf("asked %v, %d reviews", w.changed, len(w.reviews.rows))
	}
}

// A host an assistant created (or made managed) reaches the runs whose
// scope selects it.
func TestAnAssistantsNewHostPausesWhatRunsOnIt(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplB: {"web9"}}
	b := w.schedule("cleanup", w.tmplB, false, "")
	host := uuid.Must(uuid.NewV7())
	token := uuid.Must(uuid.NewV7())
	c := assistantchange.Change{Subject: assistantchange.SubjectHost, ID: host, Name: "web9", UserID: uuid.Must(uuid.NewV7()), TokenID: &token,
		Fields: []assistantchange.Field{{Name: "host", Old: nil, New: "web9"}}, Reach: &assistantchange.Reach{Hosts: []uuid.UUID{host}}}
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	if !w.schedules[b].Paused || !strings.Contains(w.events[0].Summary, "an assistant changed host web9 (host)") {
		t.Fatalf("schedule %+v, events %+v", w.schedules[b], w.events)
	}
}

// A template that starts a reached one through follow-ups is paused too
// (C2's reverse closure).
func TestAnInventoryChangeReachesTheTemplatesThatStartIt(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplB: {"web1"}}
	w.reaching = map[uuid.UUID][]uuid.UUID{w.tmplB: {w.tmplA}}
	a := w.schedule("starts cleanup", w.tmplA, false, "")
	hook := w.trigger("starts cleanup", &w.tmplA, nil, true)
	if err := w.reviewer.Changed(ctx, w.org, w.groupChange(portChange)); err != nil {
		t.Fatal(err)
	}
	if !w.schedules[a].Paused || w.triggers[hook].Enabled {
		t.Fatalf("starter's schedule %+v, trigger %+v", w.schedules[a], w.triggers[hook])
	}
}

// A change no template's run reaches pauses nothing.
func TestAnInventoryChangeNobodyRunsPausesNothing(t *testing.T) {
	w := newReviewWorld()
	a := w.schedule("nightly", w.tmplA, false, "")
	w.trigger("hook", &w.tmplA, nil, true)
	if err := w.reviewer.Changed(ctx, w.org, w.groupChange(portChange)); err != nil {
		t.Fatal(err)
	}
	if w.schedules[a].Paused || len(w.reviews.rows) != 0 || len(w.events) != 0 {
		t.Fatalf("schedule %+v, %d reviews, %d events", w.schedules[a], len(w.reviews.rows), len(w.events))
	}
}

// The review's picture shows an inventory change with its group and host.
func TestAReviewPictureShowsTheInventoryChange(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplA: {"web1"}}
	w.schedule("nightly", w.tmplA, false, "")
	host := uuid.Must(uuid.NewV7())
	c := w.groupChange(assistantchange.Field{Name: "vars.pw", Old: map[string]any{"$secret": "set"}, New: nil})
	c.Subject, c.HostID, c.HostName = assistantchange.SubjectInventoryMembership, &host, "web1"
	c.Reach = &assistantchange.Reach{Hosts: []uuid.UUID{host}}
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	objs := runrequests.ChangedObjectsOf(w.reviews.rows[0].Review.Changes)
	if len(objs) != 1 || objs[0].Subject != "inventory_membership" || objs[0].Name != "web" || objs[0].HostID != host.String() ||
		objs[0].HostName != "web1" || objs[0].Fields[0].Field != "vars.pw" {
		t.Fatalf("picture changes = %+v", objs)
	}
}
