package mcp

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// The monitoring tools (spec 2026-11-17 assistant token edit, "Part 2 —
// Tools"): reading the fleet's agents, checks, rules, alerts and
// maintenance windows (monitoring.read), and handling an alert
// (alerts.handle). Each one is the REST route it names, dispatched
// in-process as the token, so it shows and does exactly what the route
// allows a person with the same rights. No tool reads the enrollment
// tokens or install commands (GET /agents/tokens).

type checkInput struct {
	CheckID string `json:"check_id" jsonschema:"the check definition's id"`
}

type monitoringGroupInput struct {
	MonitoringGroupID string `json:"monitoring_group_id" jsonschema:"the monitoring group's id"`
}

type alertRuleInput struct {
	RuleID string `json:"rule_id" jsonschema:"the alert rule's id"`
}

type alertRuleListInput struct {
	Scope    string `json:"scope,omitempty" jsonschema:"filter by scope: org, group, host or item"`
	CheckKey string `json:"check_key,omitempty" jsonschema:"filter by the plugin's check key"`
	HostID   string `json:"host_id,omitempty" jsonschema:"filter by host id"`
}

type hostMetricsArgs struct {
	HostID  string `json:"host_id" jsonschema:"the host's id"`
	Check   string `json:"check,omitempty" jsonschema:"the check key, as list_host_checks reports it"`
	Item    string `json:"item,omitempty" jsonschema:"the mount or unit; omit for a whole-host metric"`
	Metric  string `json:"metric,omitempty" jsonschema:"the metric name, as list_host_checks reports it (for example used_percent)"`
	Range   string `json:"range,omitempty" jsonschema:"one of 1h, 24h, 7d, 30d, 90d"`
	Vantage string `json:"vantage,omitempty" jsonschema:"one vantage point's own series: manager, or the probing agent's host id"`
}

type alertListArgs struct {
	pageInput
	State        string `json:"state,omitempty" jsonschema:"open or closed"`
	Severity     string `json:"severity,omitempty" jsonschema:"warn, crit or unknown"`
	HostID       string `json:"host_id,omitempty" jsonschema:"only this host's alerts"`
	Scope        string `json:"scope,omitempty" jsonschema:"tuple or host"`
	Acknowledged *bool  `json:"acknowledged,omitempty" jsonschema:"true for only acknowledged alerts, false for only unacknowledged ones"`
	Muted        *bool  `json:"muted,omitempty" jsonschema:"true for only snoozed alerts, false for only alerts that are not snoozed"`
}

type alertInput struct {
	AlertID string `json:"alert_id" jsonschema:"the alert's id"`
}

type maintenanceInput struct {
	WindowID string `json:"window_id" jsonschema:"the maintenance window's id"`
}

type alertAckArgs struct {
	AlertID string `json:"alert_id" jsonschema:"the alert's id"`
	Note    string `json:"note,omitempty" jsonschema:"why it is acknowledged (at most 1000 characters)"`
}

type alertSnoozeArgs struct {
	AlertID     string `json:"alert_id" jsonschema:"the alert's id"`
	Until       string `json:"until,omitempty" jsonschema:"mute until this instant (RFC 3339); not together with until_change"`
	UntilChange bool   `json:"until_change,omitempty" jsonschema:"mute until the alert's severity next changes; not together with until"`
}

func setIf(q url.Values, key, v string) {
	if v != "" {
		q.Set(key, v)
	}
}

func monitoringTools() []toolDef {
	const read = tenancy.AreaMonitoringRead
	const handle = tenancy.AreaAlertsHandle
	return []toolDef{
		define("list_agents", "List agents", "The monitoring agents of the organization with their host, version, connection and last heartbeat.",
			read, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/agents")
			}),
		define("get_host_agent", "Get host agent", "The monitoring agent installed on one host.",
			read, readTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				return passGet(ctx, c, nil, "/hosts/%s/agent", in.HostID)
			}),
		define("list_checks", "List checks", "The check definitions of the organization, with their plugin, parameters, interval and where they are bound.",
			read, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/checks")
			}),
		define("get_check", "Get check", "One check definition.",
			read, readTool, func(ctx context.Context, c *caller, in checkInput) (any, error) {
				return passGet(ctx, c, nil, "/checks/%s", in.CheckID)
			}),
		define("list_host_checks", "List host checks", "The checks effective on a host with their latest results.",
			read, readTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				return passGet(ctx, c, nil, "/hosts/%s/checks", in.HostID)
			}),
		define("get_host_metrics", "Get host metrics", "The recorded series of one host metric over a range. Take check, item and metric from list_host_checks.",
			read, readTool, func(ctx context.Context, c *caller, in hostMetricsArgs) (any, error) {
				q := url.Values{}
				setIf(q, "check", in.Check)
				setIf(q, "item", in.Item)
				setIf(q, "metric", in.Metric)
				setIf(q, "range", in.Range)
				setIf(q, "vantage", in.Vantage)
				return passGet(ctx, c, q, "/hosts/%s/metrics", in.HostID)
			}),
		define("list_monitoring_groups", "List monitoring groups", "The monitoring groups: sets of hosts that checks and rules can be bound to.",
			read, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/monitoring-groups")
			}),
		define("get_monitoring_group", "Get monitoring group", "One monitoring group with its selector.",
			read, readTool, func(ctx context.Context, c *caller, in monitoringGroupInput) (any, error) {
				return passGet(ctx, c, nil, "/monitoring-groups/%s", in.MonitoringGroupID)
			}),
		define("list_alert_rules", "List alert rules", "The alert rules of the organization, optionally filtered by scope, check key or host.",
			read, readTool, func(ctx context.Context, c *caller, in alertRuleListInput) (any, error) {
				q := url.Values{}
				setIf(q, "scope", in.Scope)
				setIf(q, "check", in.CheckKey)
				setIf(q, "host", in.HostID)
				return passGet(ctx, c, q, "/rules")
			}),
		define("get_alert_rule", "Get alert rule", "One alert rule.",
			read, readTool, func(ctx context.Context, c *caller, in alertRuleInput) (any, error) {
				return passGet(ctx, c, nil, "/rules/%s", in.RuleID)
			}),
		define("get_host_effective_rules", "Get host effective rules", "The alert rules that apply to a host after scope overrides.",
			read, readTool, func(ctx context.Context, c *caller, in hostInput) (any, error) {
				return passGet(ctx, c, nil, "/hosts/%s/effective-rules", in.HostID)
			}),
		define("list_alerts", "List alerts", "The alerts, newest first, a page at a time. Filter by state, severity, host, scope, acknowledged or muted.",
			read, readTool, func(ctx context.Context, c *caller, in alertListArgs) (any, error) {
				q := in.query()
				setIf(q, "state", in.State)
				setIf(q, "severity", in.Severity)
				setIf(q, "host", in.HostID)
				setIf(q, "scope", in.Scope)
				if in.Acknowledged != nil {
					q.Set("acknowledged", strconv.FormatBool(*in.Acknowledged))
				}
				if in.Muted != nil {
					q.Set("muted", strconv.FormatBool(*in.Muted))
				}
				return passGet(ctx, c, q, "/alerts")
			}),
		define("get_alert", "Get alert", "One alert with its timeline and, when nothing was notified, why.",
			read, readTool, func(ctx context.Context, c *caller, in alertInput) (any, error) {
				return passGet(ctx, c, nil, "/alerts/%s", in.AlertID)
			}),
		define("list_maintenance_windows", "List maintenance windows", "The maintenance windows that mute alerts for the hosts they cover, and which are active now.",
			read, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				return passGet(ctx, c, nil, "/maintenance-windows")
			}),
		define("get_maintenance_window", "Get maintenance window", "One maintenance window.",
			read, readTool, func(ctx context.Context, c *caller, in maintenanceInput) (any, error) {
				return passGet(ctx, c, nil, "/maintenance-windows/%s", in.WindowID)
			}),

		define("acknowledge_alert", "Acknowledge alert", "Mark an alert as seen, with an optional note.",
			handle, writeTool, func(ctx context.Context, c *caller, in alertAckArgs) (any, error) {
				return send(ctx, c, http.MethodPost, map[string]any{"note": in.Note}, "", "/alerts/%s/acknowledge", in.AlertID)
			}),
		define("unacknowledge_alert", "Unacknowledge alert", "Take the acknowledgement off an alert.",
			handle, writeTool, func(ctx context.Context, c *caller, in alertInput) (any, error) {
				return send(ctx, c, http.MethodDelete, nil, "", "/alerts/%s/acknowledge", in.AlertID)
			}),
		define("snooze_alert", "Snooze alert", "Mute an alert until an instant (until) or until its severity next changes (until_change); give one of the two.",
			handle, writeTool, func(ctx context.Context, c *caller, in alertSnoozeArgs) (any, error) {
				body := map[string]any{}
				if in.Until != "" {
					body["until"] = in.Until
				}
				if in.UntilChange {
					body["until_change"] = true
				}
				return send(ctx, c, http.MethodPost, body, "", "/alerts/%s/snooze", in.AlertID)
			}),
		define("unsnooze_alert", "Unsnooze alert", "End an alert's snooze.",
			handle, writeTool, func(ctx context.Context, c *caller, in alertInput) (any, error) {
				return send(ctx, c, http.MethodDelete, nil, "", "/alerts/%s/snooze", in.AlertID)
			}),
	}
}
