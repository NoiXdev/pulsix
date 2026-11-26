package api

import (
	"time"

	"github.com/noixdev/pulsix/internal/triggers"
)

type triggerMappingDTO struct {
	ExtraVars map[string]string `json:"extra_vars,omitempty" doc:"var name → JSONPath ($, .name, ['name'], [index], [*])"`
	Limit     string            `json:"limit,omitempty"`
}

type rateLimitDTO struct {
	Count int    `json:"count" minimum:"1" maximum:"1000"`
	Per   string `json:"per" doc:"Duration such as 1m or 1h (1s-24h)"`
}

type triggerDTO struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind" enum:"template_run,repo_sync"`
	TemplateID      string            `json:"template_id,omitempty"`
	RepoID          string            `json:"repo_id,omitempty"`
	Name            string            `json:"name"`
	Enabled         bool              `json:"enabled"`
	Preset          string            `json:"preset" enum:"pulsix,github,gitea,gitlab,bearer"`
	Auth            string            `json:"auth" enum:"hmac,token"`
	SignatureHeader string            `json:"signature_header"`
	TimestampHeader string            `json:"timestamp_header,omitempty"`
	AllowCIDRs      []string          `json:"allow_cidrs"`
	Mapping         triggerMappingDTO `json:"mapping"`
	OnActive        string            `json:"on_active" enum:"queue,skip"`
	RateLimit       rateLimitDTO      `json:"rate_limit"`
	SettleSeconds   int               `json:"settle_seconds" doc:"Seconds the trigger waits for a quiet moment: deliveries are collected into one run that starts this long after the last of them, with the last one's values; 0 starts a run per delivery"`
	MaxWaitSeconds  *int              `json:"max_wait_seconds,omitempty" doc:"The collected run starts at the latest this long after the first delivery; absent: ten times settle_seconds"`
	URL             string            `json:"url"`
	// DisabledReason and ReviewRequestID: an assistant's change to what
	// the trigger runs or syncs disabled it until a person reviews it
	// (spec 2026-11-25 C2).
	DisabledReason  string    `json:"disabled_reason,omitempty" enum:"assistant_change" doc:"Why a disabled trigger stopped: assistant_change when an assistant changed what it runs or syncs and a person has to review it; absent otherwise"`
	ReviewRequestID string    `json:"review_request_id,omitempty" doc:"The newest review request of a trigger disabled for review"`
	Revision        int64     `json:"revision"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type triggerDeliveryDTO struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	SourceIP   string    `json:"source_ip,omitempty"`
	Outcome    string    `json:"outcome" enum:"triggered,skipped_active,rejected_ip,rejected_auth,rate_limited,invalid_payload,disabled,failed,collected" doc:"collected: joined the trigger's pending run (pending_id), which starts once the deliveries settle; job_group_id names that run once it started, superseded_by the run that took its place when the settle delay was turned off meanwhile"`
	Reason     string    `json:"reason,omitempty"`
	JobGroupID string    `json:"job_group_id,omitempty" doc:"The run the delivery started, or (collected) collapsed into"`
	PendingID  string    `json:"pending_id,omitempty" doc:"collected: the pending run it joined; it is still waiting while the trigger's pending run has this id"`
	// SupersededBy is read from the note the trigger service appends.
	SupersededBy string `json:"superseded_by,omitempty" doc:"collected: the run a later delivery started in place of the pending run, after the settle delay was turned off"`
}

// pendingRunDTO is a trigger's collected run while it waits; the collected
// values are never shown.
type pendingRunDTO struct {
	ID         string    `json:"id" doc:"The pending run; its deliveries carry it as pending_id"`
	FirstAt    time.Time `json:"first_at"`
	LastAt     time.Time `json:"last_at"`
	DueAt      time.Time `json:"due_at" doc:"When it starts unless more deliveries arrive (or a run the trigger started is still active then)"`
	DeadlineAt time.Time `json:"deadline_at" doc:"The latest it starts at, a still active run of the trigger aside"`
	Deliveries int       `json:"deliveries"`
}

// TriggerBody is the create/update body.
type TriggerBody struct {
	Kind            string             `json:"kind" enum:"template_run,repo_sync"`
	TemplateID      string             `json:"template_id,omitempty"`
	RepoID          string             `json:"repo_id,omitempty"`
	Name            string             `json:"name" minLength:"1" maxLength:"100"`
	Enabled         *bool              `json:"enabled,omitempty" doc:"default true"`
	Preset          string             `json:"preset" enum:"pulsix,github,gitea,gitlab,bearer"`
	TimestampHeader string             `json:"timestamp_header,omitempty" maxLength:"64"`
	AllowCIDRs      []string           `json:"allow_cidrs,omitempty" maxItems:"50"`
	Mapping         *triggerMappingDTO `json:"mapping,omitempty"`
	OnActive        string             `json:"on_active,omitempty" enum:"queue,skip"`
	RateLimit       *rateLimitDTO      `json:"rate_limit,omitempty"`
	SettleSeconds   *int               `json:"settle_seconds,omitempty" minimum:"0" maximum:"3600" doc:"0 (off) or 5-3600 seconds to wait for a quiet moment before one run starts for the deliveries collected meanwhile; left out on an update keeps it"`
	MaxWaitSeconds  *int               `json:"max_wait_seconds,omitempty" minimum:"0" maximum:"86400" doc:"With a settle delay: the longest the collected run waits after the first delivery, settle_seconds-86400; 0 is the default, ten times settle_seconds; left out on an update keeps it (turning the settle delay off drops it)"`
}

func toTriggerDTO(t triggers.Trigger, url string) triggerDTO {
	dto := triggerDTO{
		ID: t.ID.String(), Kind: string(t.Kind), Name: t.Name, Enabled: t.Enabled, Preset: string(t.Preset),
		Auth: string(t.Auth()), SignatureHeader: t.SignatureHeader(), TimestampHeader: t.TimestampHeader,
		AllowCIDRs: make([]string, len(t.AllowCIDRs)), Mapping: triggerMappingDTO{ExtraVars: t.Mapping.ExtraVars, Limit: t.Mapping.Limit},
		OnActive: string(t.OnActive), RateLimit: rateLimitDTO{Count: t.RateLimit.Count, Per: t.RateLimit.Per},
		URL: url, Revision: t.Revision, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, DisabledReason: t.DisabledReason,
		SettleSeconds: t.SettleSeconds, MaxWaitSeconds: t.MaxWaitSeconds,
	}
	for i, p := range t.AllowCIDRs {
		dto.AllowCIDRs[i] = p.String()
	}
	if t.TemplateID != nil {
		dto.TemplateID = t.TemplateID.String()
	}
	if t.RepoID != nil {
		dto.RepoID = t.RepoID.String()
	}
	return dto
}

func toTriggerDeliveryDTO(d triggers.Delivery) triggerDeliveryDTO {
	dto := triggerDeliveryDTO{ID: d.ID.String(), At: d.At, SourceIP: d.SourceIP, Outcome: string(d.Outcome), Reason: d.Reason}
	if d.JobGroupID != nil {
		dto.JobGroupID = d.JobGroupID.String()
	}
	if d.PendingID != nil {
		dto.PendingID = d.PendingID.String()
	}
	if run := d.SupersededBy(); run != nil {
		dto.SupersededBy = run.String()
	}
	return dto
}

func toPendingRunDTO(p triggers.PendingRun) *pendingRunDTO {
	return &pendingRunDTO{ID: p.ID.String(), FirstAt: p.FirstAt, LastAt: p.LastAt, DueAt: p.DueAt, DeadlineAt: p.DeadlineAt, Deliveries: p.Deliveries}
}

func triggerInput(b TriggerBody) (triggers.Input, error) {
	in := triggers.Input{
		Kind: triggers.Kind(b.Kind), Name: b.Name, Enabled: b.Enabled == nil || *b.Enabled, Preset: triggers.Preset(b.Preset),
		TimestampHeader: b.TimestampHeader, AllowCIDRs: b.AllowCIDRs, OnActive: triggers.OnActive(b.OnActive),
	}
	in.SettleSeconds, in.MaxWaitSeconds = triggers.ChangeSettle(0, nil, b.SettleSeconds, b.MaxWaitSeconds)
	var err error
	if in.TemplateID, err = optionalUUID(b.TemplateID, "template id"); err != nil {
		return in, err
	}
	if in.RepoID, err = optionalUUID(b.RepoID, "repo id"); err != nil {
		return in, err
	}
	if b.Mapping != nil {
		in.Mapping = triggers.Mapping{ExtraVars: b.Mapping.ExtraVars, Limit: b.Mapping.Limit}
	}
	if b.RateLimit != nil {
		in.RateLimit = triggers.RateLimit{Count: b.RateLimit.Count, Per: b.RateLimit.Per}
	}
	return in, nil
}

func triggerOut(deps Deps, t triggers.Trigger) *triggerOutput {
	out := &triggerOutput{ETag: revisionETag(t.Revision)}
	out.Body.Trigger = toTriggerDTO(t, deps.Triggers.URL(t))
	return out
}

func triggerSecret(c triggers.Created) *triggerSecretOutput {
	out := &triggerSecretOutput{}
	out.Body.Trigger, out.Body.Secret, out.Body.URL = toTriggerDTO(c.Trigger, c.URL), c.Secret, c.URL
	return out
}
