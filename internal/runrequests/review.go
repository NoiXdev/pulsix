package runrequests

import (
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

// ReviewTTL is how long a review stays approvable. Another assistant change
// of what the item runs starts it again. A review that expires leaves its
// item paused, as a rejection does; a person can still resume or enable
// the item directly.
const ReviewTTL = 30 * 24 * time.Hour

// MaxReviewChanges caps the changes one review lists: the newest are kept.
const MaxReviewChanges = 20

// ReviewChange is what a review request shows (its payload): the
// assistant changes, oldest first, that paused its schedule or disabled
// its trigger (spec 2026-11-25 C2). Never a secret: a secret value is the
// masked marker.
type ReviewChange struct {
	Changes []assistantchange.Change `json:"changes"`
	// PausedBefore is set when a person had paused the schedule (disabled
	// the trigger) before the first change: an approval then accepts the
	// changes and leaves the item stopped as that person left it.
	PausedBefore *PausedBefore `json:"paused_before,omitempty"`
}

// PausedBefore says who had stopped an item before an assistant's change;
// By is nil when no record names them.
type PausedBefore struct {
	By *uuid.UUID `json:"by,omitempty"`
	// AssistantToken names the assistant token the stop was made with;
	// By is then unset (the token's owner did not stop it).
	AssistantToken string `json:"assistant_token,omitempty"`
}

// PictureReview is the picture of a review request: the paused schedule
// or the disabled trigger as it is now, and the assistant changes that
// stopped it. Approving resumes or enables the item at the revision shown.
type PictureReview struct {
	// Item is "schedule" or "trigger"; ItemID its id.
	Item   string `json:"item" enum:"schedule,trigger" doc:"What the review resumes: a schedule or a webhook trigger"`
	ItemID string `json:"item_id"`
	// Schedule and Trigger are the item as it is now (one of them; nil
	// when it is gone). The trigger never shows its secret.
	Schedule *ScheduleState `json:"schedule,omitempty" doc:"The paused schedule as it is now"`
	Trigger  *TriggerState  `json:"trigger,omitempty" doc:"The disabled webhook trigger as it is now; never its secret"`
	// PausedForReview is whether the item is still stopped for this
	// review: false once a person resumed or paused it otherwise (an
	// approval then changes nothing about it).
	PausedForReview bool `json:"paused_for_review" doc:"Whether the item is still paused (disabled) because of the changes; approving resumes (enables) it"`
	// Changes are the assistant changes, oldest first.
	Changes []ChangedObject `json:"changes" doc:"The assistant's changes that paused the item: the job template, repository, playbook, inventory group or membership, each field's old and new value (secrets masked) and the token"`
	// Gone is set when the item, or what it starts, no longer exists.
	Gone bool `json:"gone,omitempty"`
	// PausedBefore is set when a person had paused (disabled) the item
	// before the assistant's change: approving then leaves it stopped.
	PausedBefore *PicturePausedBefore `json:"paused_before,omitempty" doc:"Set when a person had paused the schedule (disabled the trigger) before the assistant's change: approving accepts the change and leaves it paused (disabled) as that person left it"`
}

// PicturePausedBefore names who had stopped the item before the change.
type PicturePausedBefore struct {
	ByUserID         string `json:"by_user_id,omitempty" doc:"The person who paused (disabled) it, when the activity log names them"`
	ByAssistantToken string `json:"by_assistant_token,omitempty" doc:"The assistant token it was paused (disabled) with, when an assistant did it"`
}

// OpenChangeReview is an open review a schedule or trigger request's
// picture names: the review, its item's kind and name.
type OpenChangeReview struct {
	ID   uuid.UUID
	Item string
	Name string
}

// PictureOpenReview is an OpenChangeReview in a picture.
type PictureOpenReview struct {
	RequestID string `json:"request_id"`
	Item      string `json:"item" enum:"schedule,trigger"`
	Name      string `json:"name" doc:"The paused schedule's or disabled trigger's name; empty when it is gone"`
}

// Review items.
const (
	ReviewItemSchedule = "schedule"
	ReviewItemTrigger  = "trigger"
)

// ChangedObject is one assistant change as a review's picture shows it
// (assistantchange.Change).
type ChangedObject struct {
	Subject string `json:"subject" enum:"job_template,repo,playbook,inventory_group,inventory_membership,host,zone" doc:"What the assistant changed: a job template, a repository, an editor playbook, an inventory group (its values, schema, name, place, jump host or members), one host's membership in a group (joining, leaving or its values), a host (created or made managed, its zone, SSH settings or jump host), or a zone's default jump host"`
	ID      string `json:"id" doc:"The changed object's id; the group's for a membership"`
	Name    string `json:"name" doc:"The changed object's name; the group's for a membership"`
	// HostID and HostName name the host of a membership change.
	HostID   string         `json:"host_id,omitempty" doc:"The host whose membership values changed (inventory_membership only)"`
	HostName string         `json:"host_name,omitempty" doc:"The host's name (inventory_membership only)"`
	Fields   []ChangedField `json:"fields"`
	UserID   string         `json:"user_id" doc:"The person whose assistant token made the change"`
	TokenID  string         `json:"token_id,omitempty" doc:"The assistant token that made the change"`
}

// ChangedField is one field an assistant change moved (assistantchange.Field).
type ChangedField struct {
	Field string `json:"field" doc:"The field; an extra var as extra_vars.<name>, an inventory value as vars.<key> (a member's as <host>: vars.<key>), a schema default as default.<field>; member, members, name, parent, jump_host, host, managed, zone, ssh_user, ssh_port and ssh_credential for who is in a group, where it sits and how runs reach a host"`
	Old   any    `json:"old" doc:"The value before; null for none. A secret is {\"$secret\":\"set\"}"`
	New   any    `json:"new" doc:"The value after; null for none. A secret is {\"$secret\":\"set\"}"`
}

// changedObjects is changes as a picture shows them.
func changedObjects(changes []assistantchange.Change) []ChangedObject {
	out := make([]ChangedObject, len(changes))
	for i, c := range changes {
		o := ChangedObject{Subject: string(c.Subject), ID: c.ID.String(), Name: c.Name, HostName: c.HostName, UserID: c.UserID.String(), Fields: make([]ChangedField, len(c.Fields))}
		if c.HostID != nil {
			o.HostID = c.HostID.String()
		}
		if c.TokenID != nil {
			o.TokenID = c.TokenID.String()
		}
		for j, f := range c.Fields {
			o.Fields[j] = ChangedField{Field: f.Name, Old: f.Old, New: f.New}
		}
		out[i] = o
	}
	return out
}
