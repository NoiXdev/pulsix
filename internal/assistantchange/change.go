// Package assistantchange describes a change an assistant made to what
// runs unattended (spec 2026-11-25 assistant API gaps, C2, decisions D5
// and D6): who made it, which job template, repository, playbook,
// inventory group or membership it touched and which of its run-relevant
// fields moved from what to what. The services that own those objects
// (templates, repos, playbooks, inventory) decide that a change is an assistant's
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

// The subjects a change can touch. Hosts joining or leaving a group are
// not among them (spec C2: everyday work); the values a group or a
// membership hands its hosts are (decision D6).
const (
	SubjectTemplate Subject = "job_template"
	SubjectRepo     Subject = "repo"
	// SubjectPlaybook is an editor playbook's text: what every template
	// that runs it runs.
	SubjectPlaybook Subject = "playbook"
	// SubjectInventoryGroup is a group's values or schema defaults: what
	// every member of the group and of its descendant groups receives.
	SubjectInventoryGroup Subject = "inventory_group"
	// SubjectInventoryMembership is the values of one host's membership in
	// a group: what that host receives. The change's ID and Name are the
	// group's, HostID and HostName the host's.
	SubjectInventoryMembership Subject = "inventory_membership"
	// SubjectHost is a host an assistant created, or made managed: the
	// runs whose scope it now falls into reach it.
	SubjectHost Subject = "host"
	// SubjectZone is a zone's default jump host: how runs reach the
	// zone's hosts.
	SubjectZone Subject = "zone"
)

// Reach is which hosts an inventory change reaches, for the watcher to
// find the runs that receive other values or run on other hosts because
// of it (decisions D6, D7). It travels with the change and is not stored.
type Reach struct {
	// Group reaches every member of this group and of its descendant
	// groups, as the inventory is after the change.
	Group *uuid.UUID
	// Hosts reaches these hosts.
	Hosts []uuid.UUID
	// FormerGroups are groups whose subtree held reached hosts before the
	// change and no longer does (a host left a group, a group moved away
	// or was deleted): a template scoped to one of them ran on them.
	FormerGroups []uuid.UUID
}

// Field is one run-relevant field the change moved: its name (a template's
// extra var as "extra_vars.<name>", an inventory value as "vars.<key>", a
// schema default as "default.<field>") and its values before and after, as a
// person reads them. A secret value is the masked marker, never the
// secret; nil is "none".
type Field struct {
	Name string `json:"field"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

// Change is one assistant change of a job template, a repository, a
// playbook, an inventory group or a membership.
type Change struct {
	Subject Subject   `json:"subject"`
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	// HostID and HostName name the host of a membership change.
	HostID   *uuid.UUID `json:"host_id,omitempty"`
	HostName string     `json:"host_name,omitempty"`
	Fields   []Field    `json:"fields"`
	// Reach is set for an inventory change (inventory subjects, hosts).
	Reach *Reach `json:"-"`
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
