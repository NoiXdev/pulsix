package runrequests

import (
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/triggers"
)

// TriggerAction is what a trigger request does (spec 2026-11-25 assistant
// API gaps, C, decision D1).
type TriggerAction string

// The trigger actions: create a webhook trigger, change one, or replace its
// secret. Disabling and deleting need no request: they only stop runs.
const (
	TriggerCreate TriggerAction = "create"
	TriggerUpdate TriggerAction = "update"
	TriggerRotate TriggerAction = "rotate"
)

// TriggerSpec is a webhook trigger as a request asks for it: the fields of
// the trigger REST body, normalised the way the trigger service stores them
// (default on_active and rate limit filled in), so the picture shows
// exactly what the approval stores. It never holds a secret: a trigger's
// secret is drawn at the approval and shown to the approving person only.
type TriggerSpec struct {
	Kind            triggers.Kind      `json:"kind"`
	TemplateID      *uuid.UUID         `json:"template_id,omitempty"`
	RepoID          *uuid.UUID         `json:"repo_id,omitempty"`
	Name            string             `json:"name"`
	Enabled         bool               `json:"enabled"`
	Preset          triggers.Preset    `json:"preset"`
	TimestampHeader string             `json:"timestamp_header,omitempty"`
	AllowCIDRs      []string           `json:"allow_cidrs"`
	Mapping         triggers.Mapping   `json:"mapping"`
	OnActive        triggers.OnActive  `json:"on_active"`
	RateLimit       triggers.RateLimit `json:"rate_limit"`
}

// TriggerPatch is an update request's change: a nil field keeps the
// trigger's value. A trigger's kind and target never change. AllowCIDRs,
// when given (an empty list included), is the complete new list; Mapping,
// when given, the complete new mapping.
type TriggerPatch struct {
	Name            *string
	Enabled         *bool
	Preset          *triggers.Preset
	TimestampHeader *string
	AllowCIDRs      *[]string
	Mapping         *triggers.Mapping
	OnActive        *triggers.OnActive
	RateLimit       *triggers.RateLimit
}

// TriggerInput is what a trigger request asks for: Spec for a create,
// TriggerID and Patch for an update, TriggerID for a rotation.
type TriggerInput struct {
	Action    TriggerAction
	TriggerID uuid.UUID
	Spec      *TriggerSpec
	Patch     *TriggerPatch
}

// TriggerChange is what a stored trigger request does, the request's
// payload: the action and, for a create or an update, the trigger as it
// will be (an update's patch merged into the trigger when the request was
// made). Kind is the trigger's, for every action: it decides the
// permission an approval needs. KeepEnabled marks an update whose patch
// left enabled out: it keeps the trigger's enabled state as it is at the
// approval (the picture shows it), so a trigger a person disabled after
// the request stays disabled. It never holds a secret.
type TriggerChange struct {
	Action      TriggerAction `json:"action"`
	Kind        triggers.Kind `json:"kind"`
	Spec        *TriggerSpec  `json:"spec,omitempty"`
	KeepEnabled bool          `json:"keep_enabled,omitempty"`
}

// PictureTrigger is the picture of a trigger request: the trigger as it
// will be (New, a create and an update) and as it is now (Current, an
// update and a rotation), so the person sees what changes. Gone is set
// when the trigger an update or rotation names no longer exists, or the
// template or repository a trigger would start is gone; such a request
// cannot be approved.
type PictureTrigger struct {
	Action    TriggerAction `json:"action" enum:"create,update,rotate" doc:"create a webhook trigger, change one, or replace its secret"`
	TriggerID string        `json:"trigger_id" doc:"The trigger the request creates (its id is taken when the request is made), changes or rotates"`
	// URL is where senders post to; it names the trigger, never its
	// secret.
	URL     string        `json:"url" doc:"Where senders post to"`
	New     *TriggerState `json:"new,omitempty" doc:"The trigger as the approval stores it (create and update)"`
	Current *TriggerState `json:"current,omitempty" doc:"The trigger as it is now (update and rotate)"`
	Gone    bool          `json:"gone,omitempty" doc:"The trigger, or what it would start, no longer exists; the request cannot be approved"`
	// OpenChangeReviews are the open reviews an assistant's change of the
	// template (repository) it runs opened (final review 5): a note to the
	// approver that it carries unreviewed changes.
	OpenChangeReviews []string `json:"open_change_reviews,omitempty" doc:"Open review requests of an assistant's changes to the template (or repository) this would run: it carries changes no person has reviewed yet"`
}

// TriggerState is one trigger in a picture: every setting, the target by
// id and name (a repository also by URL and branch, which decide what its
// sync fetches), never the secret.
type TriggerState struct {
	Kind            string             `json:"kind" enum:"template_run,repo_sync"`
	TemplateID      string             `json:"template_id,omitempty"`
	TemplateName    string             `json:"template_name,omitempty"`
	RepoID          string             `json:"repo_id,omitempty"`
	RepoName        string             `json:"repo_name,omitempty"`
	RepoURL         string             `json:"repo_url,omitempty"`
	RepoBranch      string             `json:"repo_branch,omitempty"`
	Name            string             `json:"name"`
	Enabled         bool               `json:"enabled"`
	Preset          string             `json:"preset" enum:"pulsix,github,gitea,gitlab,bearer"`
	SignatureHeader string             `json:"signature_header" doc:"The header senders put the signature or token in"`
	TimestampHeader string             `json:"timestamp_header,omitempty"`
	AllowCIDRs      []string           `json:"allow_cidrs" doc:"The addresses senders may call from; empty allows any"`
	Mapping         triggers.Mapping   `json:"mapping" doc:"Payload JSONPaths mapped onto extra vars and the limit; a run receives the values as untrusted text"`
	OnActive        string             `json:"on_active" enum:"queue,skip"`
	RateLimit       triggers.RateLimit `json:"rate_limit"`
	// Revision is the stored trigger's (Current only): a change to the
	// trigger changes the picture, and the approval writes exactly the
	// revision the person saw.
	Revision int64 `json:"revision,omitempty"`
}
