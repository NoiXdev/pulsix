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

// List implements ReviewTemplates: the world's two templates with their
// limits.
func (w *reviewWorld) List(_ context.Context, _ uuid.UUID) ([]templates.Template, error) {
	group := uuid.Must(uuid.NewV7())
	return []templates.Template{
		{ID: w.tmplA, Name: "deploy", Limit: w.limits[w.tmplA], Scope: templates.Scope{GroupID: &group}},
		{ID: w.tmplB, Name: "cleanup", Limit: w.limits[w.tmplB]},
	}, nil
}

// Overlap implements ReviewReach: the hosts each template reaches, as the
// test set them in overlap; changed records what was asked.
func (w *reviewWorld) Overlap(_ context.Context, _ uuid.UUID, changed render.Scope, scopes map[uuid.UUID]render.Scope) (map[uuid.UUID][]string, error) {
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
	return assistantchange.Change{Subject: assistantchange.SubjectInventoryGroup, ID: uuid.Must(uuid.NewV7()), Name: "web",
		UserID: uuid.Must(uuid.NewV7()), TokenID: &token, Fields: fields}
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
	if len(w.changed) != 1 || w.changed[0].GroupID == nil || *w.changed[0].GroupID != c.ID || len(w.changed[0].HostIDs) != 0 {
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
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	if len(w.changed) != 1 || w.changed[0].GroupID != nil || len(w.changed[0].HostIDs) != 1 || w.changed[0].HostIDs[0] != host {
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

// The limit decides with the template's scope: a template whose limit
// keeps none of the reached hosts is not paused, one whose limit cannot be
// decided is (fail closed). A schedule's own limit replaces the
// template's, and a trigger that maps its limit from the payload cannot be
// decided.
func TestAnInventoryChangeHonoursTheLimit(t *testing.T) {
	w := newReviewWorld()
	w.overlap = map[uuid.UUID][]string{w.tmplA: {"web1"}, w.tmplB: {"web1"}}
	w.limits = map[uuid.UUID]string{w.tmplA: "db*", w.tmplB: "~web.*"}
	excluded := w.schedule("db only", w.tmplA, false, "")
	own := w.schedule("own limit", w.tmplA, false, "")
	s := w.schedules[own]
	limit := "web1"
	s.Overrides.Limit = &limit
	w.schedules[own] = s
	ownElsewhere := w.schedule("own limit elsewhere", w.tmplA, false, "")
	s = w.schedules[ownElsewhere]
	other := "db2"
	s.Overrides.Limit = &other
	w.schedules[ownElsewhere] = s
	undecidable := w.schedule("regex", w.tmplB, false, "")
	plain := w.trigger("plain", &w.tmplA, nil, true)
	mapped := w.trigger("mapped", &w.tmplA, nil, true)
	tr := w.triggers[mapped]
	tr.Mapping.Limit = "$.host"
	w.triggers[mapped] = tr
	if err := w.reviewer.Changed(ctx, w.org, w.groupChange(portChange)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]bool{excluded: false, own: true, ownElsewhere: false, undecidable: true} {
		if got := w.schedules[id].Paused; got != want {
			t.Errorf("schedule %s paused = %v, want %v", w.schedules[id].Name, got, want)
		}
	}
	if !w.triggers[plain].Enabled || w.triggers[mapped].Enabled {
		t.Errorf("triggers: plain enabled %v (want true), mapped enabled %v (want false)", w.triggers[plain].Enabled, w.triggers[mapped].Enabled)
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
	if err := w.reviewer.Changed(ctx, w.org, c); err != nil {
		t.Fatal(err)
	}
	objs := runrequests.ChangedObjectsOf(w.reviews.rows[0].Review.Changes)
	if len(objs) != 1 || objs[0].Subject != "inventory_membership" || objs[0].Name != "web" || objs[0].HostID != host.String() ||
		objs[0].HostName != "web1" || objs[0].Fields[0].Field != "vars.pw" {
		t.Fatalf("picture changes = %+v", objs)
	}
}
