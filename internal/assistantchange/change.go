// Package assistantchange describes a change an assistant made to what
// runs unattended (spec 2026-11-25 assistant API gaps, C2, decision D5):
// who made it, which job template or repository it touched and which of
// its run-relevant fields moved from what to what. The services that own
// those objects (templates, repos) decide that a change is an assistant's
// and run-relevant, and hand it to a Watcher inside the change's own
// transaction; the watcher pauses what runs the object until a person
// reviews it. The types live here, apart from both, so neither owner
// depends on the other or on the watcher.
package assistantchange

import (
	"context"

	"github.com/google/uuid"
)

// Actor is who makes a change. Assistant is true when the change is made
// with an assistant token; a person's change (session or API token) never
// pauses anything.
type Actor struct {
	UserID    uuid.UUID
	TokenID   *uuid.UUID
	Assistant bool
}

// Subject is the kind of object a change touched.
type Subject string

// The subjects a change can touch. Inventory changes are not among them
// (spec C2: hosts joining or leaving a group are everyday work).
const (
	SubjectTemplate Subject = "job_template"
	SubjectRepo     Subject = "repo"
)

// Field is one run-relevant field the change moved: its name (a template's
// extra var as "extra_vars.<name>") and its values before and after, as a
// person reads them. A secret value is the masked marker, never the
// secret; nil is "none".
type Field struct {
	Name string `json:"field"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

// Change is one assistant change of a job template or a repository.
type Change struct {
	Subject Subject   `json:"subject"`
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Fields  []Field   `json:"fields"`
	// UserID and TokenID are the actor's (Actor without its kind: a
	// change handed on is always an assistant's).
	UserID  uuid.UUID  `json:"user_id"`
	TokenID *uuid.UUID `json:"token_id,omitempty"`
}

// New is the Change of subject id (named name) moving fields, made by
// actor.
func New(subject Subject, id uuid.UUID, name string, fields []Field, actor Actor) Change {
	return Change{Subject: subject, ID: id, Name: name, Fields: fields, UserID: actor.UserID, TokenID: actor.TokenID}
}

// Watcher is told about an assistant's run-relevant change inside the
// transaction that writes it, with the context bound to that transaction:
// whatever it writes commits with the change or not at all, and an error
// rolls the change back.
type Watcher interface {
	Changed(ctx context.Context, orgID uuid.UUID, c Change) error
}
