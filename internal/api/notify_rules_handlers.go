package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/notify"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type notifyRuleBody struct {
	On       string   `json:"on" enum:"failure,change,success,skipped"`
	Channels []string `json:"channels" minItems:"1" maxItems:"20" doc:"Channel ids"`
}

// NotifyRulesBody replaces a schedule's notification rules.
type NotifyRulesBody struct {
	Rules []notifyRuleBody `json:"rules" maxItems:"16"`
}

type notifyRuleChannelDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind" enum:"email,webhook"`
}

type notifyRuleDTO struct {
	On       string                 `json:"on" enum:"failure,change,success,skipped"`
	Channels []notifyRuleChannelDTO `json:"channels"`
}

type notifyRulesOutput struct {
	Body struct {
		Rules []notifyRuleDTO `json:"rules"`
	}
}

type notifyRulesInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body NotifyRulesBody
}

// registerScheduleNotifyRoutes wires a schedule's notify_rules (spec
// §7) as a sub-resource: reading them needs templates.read like the
// schedule itself, replacing them templates.write. A run yields at most one
// message per channel however many rules name the channel.
func registerScheduleNotifyRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/schedules/{id}/notifications", func(ctx context.Context, in *scheduleIDInput) (*notifyRulesOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesRead)
		if err != nil {
			return nil, err
		}
		if _, err := deps.Schedules.Get(ctx, scope.OrgID, id); err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		rules, err := deps.Notify.ScheduleRules(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		return notifyRulesOut(rules), nil
	})

	huma.Put(hapi, "/api/v1/orgs/{org}/schedules/{id}/notifications", func(ctx context.Context, in *notifyRulesInput) (*notifyRulesOutput, error) {
		scope, id, err := scheduleScope(ctx, deps, in.Org, in.ID, tenancy.PermTemplatesWrite)
		if err != nil {
			return nil, err
		}
		sch, err := deps.Schedules.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapSchedulesErr(ctx, err)
		}
		rules := make([]notify.Rule, len(in.Body.Rules))
		for i, r := range in.Body.Rules {
			rules[i].On = notify.Category(r.On)
			for _, raw := range r.Channels {
				ch, err := parseUUID(raw, "channel id")
				if err != nil {
					return nil, err
				}
				rules[i].Channels = append(rules[i].Channels, ch)
			}
		}
		stored, err := deps.Notify.SetScheduleRules(ctx, scope.OrgID, id, rules)
		if err != nil {
			return nil, mapNotifyErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), "updated the notifications of schedule "+sch.Name)
		return notifyRulesOut(stored), nil
	}, withAudit("setScheduleNotifications", "schedule"))
}

func notifyRulesOut(rules []notify.RuleView) *notifyRulesOutput {
	out := &notifyRulesOutput{}
	out.Body.Rules = make([]notifyRuleDTO, len(rules))
	for i, r := range rules {
		dto := notifyRuleDTO{On: string(r.On), Channels: make([]notifyRuleChannelDTO, len(r.Channels))}
		for j, c := range r.Channels {
			dto.Channels[j] = notifyRuleChannelDTO{ID: c.ID.String(), Name: c.Name, Kind: string(c.Kind)}
		}
		out.Body.Rules[i] = dto
	}
	return out
}
