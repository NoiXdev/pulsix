package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// The ops tools (spec 2026-11-17 assistant token edit, "Part 2 — Tools"):
// schedules and the calendar (templates.read), the activity log
// (activity.read), a host's manual tags and its connection test
// (inventory.write / inventory.read). Each one is the REST route it names,
// dispatched in-process as the token.

type scheduleInput struct {
	ScheduleID string `json:"schedule_id" jsonschema:"the schedule's id"`
}

type scheduleListArgs struct {
	TemplateID string `json:"template_id,omitempty" jsonschema:"only this job template's schedules"`
}

type scheduleRunsArgs struct {
	ScheduleID string `json:"schedule_id" jsonschema:"the schedule's id"`
	Limit      int    `json:"limit,omitempty" jsonschema:"how many runs (1 to 100)"`
}

type calendarArgs struct {
	From string `json:"from" jsonschema:"start of the range (RFC 3339)"`
	To   string `json:"to" jsonschema:"end of the range, exclusive (RFC 3339), at most 6 weeks after from"`
}

type activityArgs struct {
	pageInput
	Actor      string `json:"actor,omitempty" jsonschema:"only this user's id"`
	ObjectType string `json:"object_type,omitempty" jsonschema:"only this kind of object, e.g. host or job_template"`
	Since      string `json:"since,omitempty" jsonschema:"only events from this instant on (RFC 3339)"`
}

type hostTagArg struct {
	Key   string `json:"key" jsonschema:"the tag's key"`
	Value string `json:"value,omitempty" jsonschema:"the tag's value; may be empty"`
}

type hostTagsArgs struct {
	HostID string       `json:"host_id" jsonschema:"the host's id"`
	Tags   []hostTagArg `json:"tags" jsonschema:"the host's complete manual tag set: it replaces the current manual tags (an empty list clears them); tags a collector reports are not touched"`
}

func opsTools() []toolDef {
	const templatesRead = tenancy.AreaTemplatesRead
	return []toolDef{
		define("list_schedules", "List schedules", "The recurring schedules of the organization's job templates, with their next and last run.",
			templatesRead, readTool, func(ctx context.Context, c *caller, in scheduleListArgs) (any, error) {
				q := url.Values{}
				setIf(q, "template_id", in.TemplateID)
				return passGet(ctx, c, q, "/schedules")
			}),
		define("get_schedule", "Get schedule", "One schedule.",
			templatesRead, readTool, func(ctx context.Context, c *caller, in scheduleInput) (any, error) {
				return passGet(ctx, c, nil, "/schedules/%s", in.ScheduleID)
			}),
		define("list_schedule_runs", "List schedule runs", "The recent runs of one schedule, newest first.",
			templatesRead, readTool, func(ctx context.Context, c *caller, in scheduleRunsArgs) (any, error) {
				q := url.Values{}
				if in.Limit > 0 {
					q.Set("limit", strconv.Itoa(in.Limit))
				}
				return passGet(ctx, c, q, "/schedules/%s/runs", in.ScheduleID)
			}),
		define("get_calendar", "Get calendar", "The schedule occurrences within a time range; with the monitoring area it also lists the maintenance windows in the range.",
			templatesRead, readTool, func(ctx context.Context, c *caller, in calendarArgs) (any, error) {
				q := url.Values{}
				q.Set("from", in.From)
				q.Set("to", in.To)
				out, err := passGet(ctx, c, q, "/calendar")
				if err != nil {
					return nil, err
				}
				if tenancy.AreasAllow(c.areas, tenancy.PermMaintenanceRead) {
					return out, nil
				}
				return withoutKey(out.(json.RawMessage), "maintenance")
			}),
		define("list_activity", "List activity", "The organization's activity log, newest first, a page at a time. Filter by actor, object type or a start time.",
			tenancy.AreaActivityRead, readTool, func(ctx context.Context, c *caller, in activityArgs) (any, error) {
				q := in.query()
				setIf(q, "actor", in.Actor)
				setIf(q, "object_type", in.ObjectType)
				setIf(q, "since", in.Since)
				return passGet(ctx, c, q, "/activity")
			}),

		define("set_host_tags", "Set host tags", "Replace a host's manual tags with exactly the given set; tags a collector reports stay.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in hostTagsArgs) (any, error) {
				// The route requires "value" on every tag, so it is always sent.
				type wireTag struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				}
				tags := make([]wireTag, len(in.Tags))
				for i, tg := range in.Tags {
					tags[i] = wireTag(tg)
				}
				return send(ctx, c, http.MethodPut, tags, "", "/hosts/%s/tags", in.HostID)
			}),
		define("start_connection_test", "Start connection test", "Test the connection to a host: it logs in and probes the host without running a playbook and pins no host key, so it needs no approval. Returns a job_id; read the outcome with get_connection_test.",
			tenancy.AreaInventoryWrite, writeTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				return send(ctx, c, http.MethodPost, nil, "", "/hosts/%s/connection-test", in.HostID)
			}),
		define("get_connection_test", "Get connection test", "The latest connection test outcome of a host.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				return passGet(ctx, c, nil, "/hosts/%s/connection-test", in.HostID)
			}),
	}
}

// withoutKey drops one top-level key of a JSON object body. It fails closed:
// a body it cannot parse or re-encode is an error, never passed through,
// because the dropped key is data the caller must not see.
func withoutKey(raw json.RawMessage, key string) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("drop %q from response: %w", key, err)
	}
	if _, ok := obj[key]; !ok {
		return raw, nil
	}
	delete(obj, key)
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("drop %q from response: %w", key, err)
	}
	return b, nil
}
