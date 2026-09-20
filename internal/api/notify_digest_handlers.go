package api

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type notifyDigestDTO struct {
	Enabled   bool                   `json:"enabled"`
	Weekday   string                 `json:"weekday" enum:"sun,mon,tue,wed,thu,fri,sat"`
	Time      string                 `json:"time" doc:"HH:MM in timezone"`
	Timezone  string                 `json:"timezone"`
	Channels  []notifyRuleChannelDTO `json:"channels"`
	NextRunAt *time.Time             `json:"next_run_at,omitempty"`
}

// NotifyDigestBody replaces the organization's weekly digest settings.
type NotifyDigestBody struct {
	Enabled  bool     `json:"enabled"`
	Weekday  string   `json:"weekday" enum:"sun,mon,tue,wed,thu,fri,sat"`
	Time     string   `json:"time" pattern:"^[0-2][0-9]:[0-5][0-9]$"`
	Timezone string   `json:"timezone" minLength:"1" maxLength:"64"`
	Channels []string `json:"channels" maxItems:"20"`
}

type notifyDigestOutput struct {
	Body struct {
		Digest notifyDigestDTO `json:"digest"`
	}
}

// registerNotifyDigestRoutes wires the weekly digest (spec §7):
// channels.read to see it, channels.write to change it.
func registerNotifyDigestRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/notification-digest", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*notifyDigestOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsRead)
		if err != nil {
			return nil, err
		}
		d, err := deps.Notify.Digest(ctx, scope.OrgID)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		return notifyDigestOut(d), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/notification-digest", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		Body NotifyDigestBody
	}) (*notifyDigestOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermChannelsWrite)
		if err != nil {
			return nil, err
		}
		channels := make([]uuid.UUID, len(in.Body.Channels))
		for i, raw := range in.Body.Channels {
			if channels[i], err = parseUUID(raw, "channel id"); err != nil {
				return nil, err
			}
		}
		d, err := deps.Notify.SetDigest(ctx, scope.OrgID, notify.DigestInput{
			Enabled: in.Body.Enabled, Weekday: in.Body.Weekday, Time: in.Body.Time, Timezone: in.Body.Timezone, Channels: channels,
		})
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, "", "updated the weekly digest")
		return notifyDigestOut(d), nil
	}, withAudit("updateNotificationDigest", "channel"))
}

func notifyDigestOut(d notify.DigestSettings) *notifyDigestOutput {
	out := &notifyDigestOutput{}
	out.Body.Digest = notifyDigestDTO{
		Enabled: d.Enabled, Weekday: notify.WeekdayName(d.Weekday), Time: d.Time, Timezone: d.Timezone,
		Channels: make([]notifyRuleChannelDTO, len(d.Channels)), NextRunAt: d.NextRunAt,
	}
	for i, c := range d.Channels {
		out.Body.Digest.Channels[i] = notifyRuleChannelDTO{ID: c.ID.String(), Name: c.Name, Kind: string(c.Kind)}
	}
	return out
}
