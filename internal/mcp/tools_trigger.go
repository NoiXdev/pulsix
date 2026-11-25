package mcp

import (
	"context"
	"net/http"
	"net/url"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// The webhook trigger tools (spec 2026-11-25 assistant API gaps, C,
// decision D1): creating a trigger, changing one and giving it a new secret
// are requests a person approves; the trigger's secret is shown to that
// person at the approval and never to the assistant. Disabling and deleting
// only stop runs and act directly. Reading needs templates.read (and
// repos.read for a repository sync trigger), as the trigger routes do.

type triggerMappingArg struct {
	ExtraVars map[string]string `json:"extra_vars,omitempty" jsonschema:"extra var name -> JSONPath into the call's JSON payload ($, .name, ['name'], [index], [*]); never a secret field or a name Ansible or Pulsix act on (ansible_..., pulsix_..., magic variables); the run receives the values as untrusted text"`
	Limit     string            `json:"limit,omitempty" jsonschema:"a JSONPath whose value becomes the run's limit"`
}

type rateLimitArg struct {
	Count int    `json:"count" jsonschema:"calls allowed per period, 1 to 1000"`
	Per   string `json:"per" jsonschema:"the period, a duration from 1s to 24h such as 1m"`
}

type triggerCreateArgs struct {
	Kind            string             `json:"kind" jsonschema:"template_run (a call runs a job template) or repo_sync (a call syncs a repository)"`
	TemplateID      string             `json:"template_id,omitempty" jsonschema:"template_run: the job template the trigger runs"`
	RepoID          string             `json:"repo_id,omitempty" jsonschema:"repo_sync: the repository the trigger syncs"`
	Name            string             `json:"name" jsonschema:"the trigger's name, unique in the organization"`
	Enabled         *bool              `json:"enabled,omitempty" jsonschema:"whether the trigger takes calls once approved (default true)"`
	Preset          string             `json:"preset" jsonschema:"how senders prove themselves: pulsix (HMAC), github, gitea, gitlab (token) or bearer"`
	TimestampHeader string             `json:"timestamp_header,omitempty" jsonschema:"pulsix preset only: a header carrying the call's timestamp, signed with the body (5 minute replay window)"`
	AllowCIDRs      []string           `json:"allow_cidrs,omitempty" jsonschema:"addresses or CIDRs senders may call from (at most 50); empty allows any"`
	Mapping         *triggerMappingArg `json:"mapping,omitempty" jsonschema:"template_run only: payload values mapped onto extra vars and the limit"`
	OnActive        string             `json:"on_active,omitempty" jsonschema:"queue (default) or skip: what a call does while a run or sync it would start is active"`
	RateLimit       *rateLimitArg      `json:"rate_limit,omitempty" jsonschema:"calls allowed per period (default 10 per minute)"`
	Reason          string             `json:"reason" jsonschema:"one line on why; the person approving reads it"`
	ExpiresIn       int                `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

type triggerUpdateArgs struct {
	TriggerID       string             `json:"trigger_id" jsonschema:"the trigger to change"`
	Name            string             `json:"name,omitempty"`
	Enabled         *bool              `json:"enabled,omitempty" jsonschema:"false alone disables the trigger at once; true (or any other change) is a request a person approves. Left out, the trigger stays as enabled or disabled as it is when the request is approved"`
	Preset          string             `json:"preset,omitempty" jsonschema:"pulsix, github, gitea, gitlab or bearer"`
	TimestampHeader *string            `json:"timestamp_header,omitempty" jsonschema:"pulsix preset only; an empty text removes it"`
	AllowCIDRs      []string           `json:"allow_cidrs,omitempty" jsonschema:"the complete new list of addresses senders may call from; an empty list allows any"`
	Mapping         *triggerMappingArg `json:"mapping,omitempty" jsonschema:"the complete new mapping (template_run only)"`
	OnActive        string             `json:"on_active,omitempty" jsonschema:"queue or skip"`
	RateLimit       *rateLimitArg      `json:"rate_limit,omitempty"`
	Reason          string             `json:"reason,omitempty" jsonschema:"one line on why; the person approving reads it (required unless only disabling)"`
	ExpiresIn       int                `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

// disablesOnly reports whether the update only turns the trigger off: that
// stops runs, so it needs no approval.
func (in triggerUpdateArgs) disablesOnly() bool {
	return in.Enabled != nil && !*in.Enabled && in.Name == "" && in.Preset == "" && in.TimestampHeader == nil &&
		in.AllowCIDRs == nil && in.Mapping == nil && in.OnActive == "" && in.RateLimit == nil
}

func (in triggerUpdateArgs) body() map[string]any {
	body := requestBody(in.Reason, in.ExpiresIn)
	putIf(body, "name", in.Name)
	putIf(body, "preset", in.Preset)
	putIf(body, "on_active", in.OnActive)
	if in.Enabled != nil {
		body["enabled"] = *in.Enabled
	}
	if in.TimestampHeader != nil {
		body["timestamp_header"] = *in.TimestampHeader
	}
	if in.AllowCIDRs != nil {
		body["allow_cidrs"] = in.AllowCIDRs
	}
	if in.Mapping != nil {
		body["mapping"] = in.Mapping
	}
	if in.RateLimit != nil {
		body["rate_limit"] = in.RateLimit
	}
	return body
}

type triggerRotateArgs struct {
	TriggerID string `json:"trigger_id" jsonschema:"the trigger that needs a new secret"`
	Reason    string `json:"reason" jsonschema:"one line on why; the person approving reads it"`
	ExpiresIn int    `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

type triggerIDArgs struct {
	TriggerID string `json:"trigger_id" jsonschema:"the trigger's id"`
}

type triggerListArgs struct {
	TemplateID string `json:"template_id,omitempty" jsonschema:"the job template whose triggers to list; give this or repo_id"`
	RepoID     string `json:"repo_id,omitempty" jsonschema:"the repository whose sync triggers to list; give this or template_id"`
}

// requestBody is a request's reason and expiry.
func requestBody(reason string, expiresIn int) map[string]any {
	body := map[string]any{"reason": reason}
	if expiresIn != 0 {
		body["expires_in"] = expiresIn
	}
	return body
}

// triggerNote is what a trigger request's result tells the assistant.
const triggerNote = "Nothing changes until a person approves this request at approval_url; the approval then creates or changes the trigger exactly as the picture shows. A new secret is shown once to the approving person, never to you: ask them to put it into the sender. get_trigger shows the URL and settings. Give them the link."

func triggerTools() []toolDef {
	const area = tenancy.AreaAutomationRequest
	const templatesRead = tenancy.AreaTemplatesRead
	return []toolDef{
		define("list_triggers", "List webhook triggers", "The webhook triggers of one job template (template_id) or repository (repo_id): URL, sender preset, allowed addresses, mapping, rate limit and whether each is enabled. Never a secret.",
			templatesRead, readTool, func(ctx context.Context, c *caller, in triggerListArgs) (any, error) {
				q := url.Values{}
				setIf(q, "template_id", in.TemplateID)
				setIf(q, "repo_id", in.RepoID)
				return passGet(ctx, c, q, "/triggers")
			}),
		define("get_trigger", "Get webhook trigger", "One webhook trigger: its URL and settings. Never its secret.",
			templatesRead, readTool, func(ctx context.Context, c *caller, in triggerIDArgs) (any, error) {
				return passGet(ctx, c, nil, "/triggers/%s", in.TriggerID)
			}),
		defineExact("create_trigger", "Create a webhook trigger", "Ask for a webhook trigger: a URL that runs a job template (template_run) or syncs a repository (repo_sync) when a sender calls it, with the sender preset, allowed addresses, a rate limit and, for a template run, a mapping of payload values onto extra vars. Nothing is created until a person approves the request in Pulsix; the trigger's secret is shown to that person only, never to you. The result carries the link to give them and the picture they see.",
			area, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in triggerCreateArgs) (any, error) {
				body := requestBody(in.Reason, in.ExpiresIn)
				body["kind"], body["name"], body["preset"] = in.Kind, in.Name, in.Preset
				putIf(body, "template_id", in.TemplateID)
				putIf(body, "repo_id", in.RepoID)
				putIf(body, "timestamp_header", in.TimestampHeader)
				putIf(body, "on_active", in.OnActive)
				if in.Enabled != nil {
					body["enabled"] = *in.Enabled
				}
				if in.AllowCIDRs != nil {
					body["allow_cidrs"] = in.AllowCIDRs
				}
				if in.Mapping != nil {
					body["mapping"] = in.Mapping
				}
				if in.RateLimit != nil {
					body["rate_limit"] = in.RateLimit
				}
				return c.requestAt(ctx, "/trigger-requests", nil, body, triggerNote)
			}),
		defineExact("update_trigger", "Change a webhook trigger", "Disable a webhook trigger at once (enabled false and nothing else), or ask for a change of one: give only the fields that change. A change, enabling included, waits for a person's approval in Pulsix; the picture shows the trigger as it is next to how it will be. The kind and target never change.",
			area, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in triggerUpdateArgs) (any, error) {
				if in.disablesOnly() {
					return send(ctx, c, http.MethodPost, nil, "", "/triggers/%s/disable", in.TriggerID)
				}
				return c.requestAt(ctx, "/triggers/%s/update-requests", []string{in.TriggerID}, in.body(), triggerNote)
			}),
		defineExact("rotate_trigger_secret", "Rotate a webhook trigger's secret", "Ask for a new secret for a webhook trigger, for instance after the old one leaked. Once a person approves the request in Pulsix, the old secret stops working and the new one is shown to that person only, never to you; the trigger stays enabled or disabled as it is.",
			area, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in triggerRotateArgs) (any, error) {
				return c.requestAt(ctx, "/triggers/%s/rotate-requests", []string{in.TriggerID}, requestBody(in.Reason, in.ExpiresIn), triggerNote)
			}),
		define("delete_trigger", "Delete a webhook trigger", "Delete a webhook trigger at once, with its delivery log: its URL stops working. Runs already started go on.",
			area, writeTool, func(ctx context.Context, c *caller, in triggerIDArgs) (any, error) {
				return send(ctx, c, http.MethodDelete, nil, "", "/triggers/%s", in.TriggerID)
			}),
	}
}
