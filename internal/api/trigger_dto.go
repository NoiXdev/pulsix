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
	URL             string            `json:"url"`
	Revision        int64             `json:"revision"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type triggerDeliveryDTO struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	SourceIP   string    `json:"source_ip,omitempty"`
	Outcome    string    `json:"outcome" enum:"triggered,skipped_active,rejected_ip,rejected_auth,rate_limited,invalid_payload,disabled,failed"`
	Reason     string    `json:"reason,omitempty"`
	JobGroupID string    `json:"job_group_id,omitempty"`
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
}

func toTriggerDTO(t triggers.Trigger, url string) triggerDTO {
	dto := triggerDTO{
		ID: t.ID.String(), Kind: string(t.Kind), Name: t.Name, Enabled: t.Enabled, Preset: string(t.Preset),
		Auth: string(t.Auth()), SignatureHeader: t.SignatureHeader(), TimestampHeader: t.TimestampHeader,
		AllowCIDRs: make([]string, len(t.AllowCIDRs)), Mapping: triggerMappingDTO{ExtraVars: t.Mapping.ExtraVars, Limit: t.Mapping.Limit},
		OnActive: string(t.OnActive), RateLimit: rateLimitDTO{Count: t.RateLimit.Count, Per: t.RateLimit.Per},
		URL: url, Revision: t.Revision, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
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
	return dto
}
