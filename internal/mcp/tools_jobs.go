package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// The job tools (spec 2026-11-16 assistant access, "Tools"): reading runs
// and their logs, and asking for a run or a retry that a person approves.

const (
	defaultLogLimit = 200
	maxLogLimit     = 1000
)

type jobListArgs struct {
	pageInput
	Status     string `json:"status,omitempty" jsonschema:"queued, running, succeeded, failed, cancelled, error ..."`
	TemplateID string `json:"template_id,omitempty"`
	Group      string `json:"run_group_id,omitempty" jsonschema:"only the jobs of this run group"`
}

type jobArgs struct {
	JobID string `json:"job_id"`
}

type jobLogArgs struct {
	JobID      string `json:"job_id"`
	After      int64  `json:"after,omitempty" jsonschema:"only events after this sequence number (the next_after of the previous call)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"events per call, default 200, at most 1000"`
	Host       string `json:"host,omitempty" jsonschema:"only this host's events"`
	FailedOnly bool   `json:"failed_only,omitempty" jsonschema:"only failed and unreachable events"`
}

type runRequestArgs struct {
	TemplateID string         `json:"template_id"`
	HostID     string         `json:"host_id,omitempty" jsonschema:"run on this one host only (or give host_ids)"`
	Reason     string         `json:"reason" jsonschema:"one line on why the run is needed; the person approving reads it"`
	CheckMode  bool           `json:"check_mode,omitempty" jsonschema:"a dry run: check mode, changes nothing (tasks marked to run in check mode still run); needs approval like any run and starts no follow-ups"`
	Diff       *bool          `json:"diff,omitempty" jsonschema:"show the changes a dry run would make; only with check_mode, on unless false"`
	ExtraVars  map[string]any `json:"extra_vars,omitempty" jsonschema:"extra vars replacing the template's top-level ones of the same name; never secrets (a secret field is refused); the run takes every text in them literally, never as a template"`
	HostIDs    []string       `json:"host_ids,omitempty" jsonschema:"run on these hosts only (at most 500), each among the template's targets"`
	Limit      string         `json:"limit,omitempty" jsonschema:"host names and globs (* ?) joined by , or :, with ! for exclusions; the run keeps to the template's targets whose name matches"`
	ExpiresIn  int            `json:"expires_in,omitempty" jsonschema:"minutes the request stays approvable, 5 to 1440, default 60"`
}

// boundExpiresIn gives request_run's expires_in its bounds in the schema.
func boundExpiresIn(s *jsonschema.Schema) {
	lo, hi := float64(5), float64(1440)
	s.Properties["expires_in"].Minimum, s.Properties["expires_in"].Maximum = &lo, &hi
}

// body is the REST body of a run request.
func (in runRequestArgs) body() map[string]any {
	body := map[string]any{"reason": in.Reason}
	for k, v := range map[string]string{"host_id": in.HostID, "limit": in.Limit} {
		if v != "" {
			body[k] = v
		}
	}
	if in.CheckMode {
		body["check_mode"] = true
	}
	if in.Diff != nil {
		body["diff"] = *in.Diff
	}
	if len(in.ExtraVars) > 0 {
		body["extra_vars"] = in.ExtraVars
	}
	if len(in.HostIDs) > 0 {
		body["host_ids"] = in.HostIDs
	}
	if in.ExpiresIn != 0 {
		body["expires_in"] = in.ExpiresIn
	}
	return body
}

type retryRequestArgs struct {
	JobID  string `json:"job_id" jsonschema:"the finished, failed run"`
	Reason string `json:"reason"`
}

type runRequestListArgs struct {
	Status string `json:"status,omitempty" jsonschema:"pending, approved, rejected or expired"`
}

type runRequestGetArgs struct {
	RequestID string `json:"request_id"`
}

// jobEvent is one event as GET /jobs/{id}/events returns it.
type jobEvent struct {
	Seq     int64          `json:"seq"`
	Attempt int            `json:"attempt"`
	Host    string         `json:"host"`
	Task    string         `json:"task"`
	State   string         `json:"state"`
	Payload map[string]any `json:"payload"`
}

var failedStates = map[string]bool{"failed": true, "unreachable": true, "error": true, "fatal": true}

// renderEvents is the log text: one line per event, filtered, at most
// limit lines, and the sequence number to continue after.
func renderEvents(events []jobEvent, after int64, host string, failedOnly bool, limit int) (string, int64) {
	var b strings.Builder
	next, n := after, 0
	for _, e := range events {
		if e.Seq <= after {
			continue
		}
		if n == limit {
			break
		}
		next = e.Seq
		if (host != "" && e.Host != host) || (failedOnly && !failedStates[e.State]) {
			continue
		}
		n++
		fmt.Fprintf(&b, "%d %s %s %s", e.Seq, e.Host, e.Task, e.State)
		if text := eventText(e.Payload); text != "" {
			b.WriteString(": " + text)
		}
		b.WriteByte('\n')
	}
	return b.String(), next
}

// eventText is the part of a payload a log line shows.
func eventText(p map[string]any) string {
	for _, k := range []string{"text", "msg", "stdout", "stderr"} {
		if s, ok := p[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func jobTools() []toolDef {
	return []toolDef{
		define("list_jobs", "List jobs", "Runs and other jobs, newest first, a page at a time.",
			tenancy.AreaJobsRead, readTool, func(ctx context.Context, c *caller, in jobListArgs) (any, error) {
				q := in.query()
				for k, v := range map[string]string{"status": in.Status, "template_id": in.TemplateID, "group": in.Group} {
					if v != "" {
						q.Set(k, v)
					}
				}
				return passGet(ctx, c, q, "/jobs")
			}),
		define("get_job", "Get job", "One job: status, hosts, stats, error and its run group.",
			tenancy.AreaJobsRead, readTool, func(ctx context.Context, c *caller, in jobArgs) (any, error) {
				return passGet(ctx, c, nil, "/jobs/%s", in.JobID)
			}),
		define("get_job_log", "Get job log", "A job's log, one line per event (sequence, host, task, state, text). Call again with next_after to read on; a running job's log grows.",
			tenancy.AreaJobsRead, readTool, func(ctx context.Context, c *caller, in jobLogArgs) (any, error) {
				limit := in.Limit
				if limit == 0 {
					limit = defaultLogLimit
				}
				if limit < 1 || limit > maxLogLimit {
					return nil, fmt.Errorf("limit must be between 1 and %d", maxLogLimit)
				}
				path, err := c.orgPath("/jobs/%s/events", in.JobID)
				if err != nil {
					return nil, err
				}
				var body struct {
					Events []jobEvent `json:"events"`
				}
				q := url.Values{}
				if in.After > 0 {
					q.Set("after", strconv.FormatInt(in.After, 10))
				}
				if _, err := c.get(ctx, path, q, &body); err != nil {
					return nil, err
				}
				text, next := renderEvents(body.Events, in.After, in.Host, in.FailedOnly, limit)
				return map[string]any{"log": text, "next_after": next}, nil
			}),
		defineExact("request_run", "Request a run", "Ask for a run of a job template, optionally as a dry run (check_mode), with non-secret extra vars, narrowed to some hosts (host_ids) or a limit. Nothing runs until a person approves the request in Pulsix; the result carries the link to give them and the picture the person sees. Requests expire after an hour unless expires_in says otherwise. Whether an approval also starts the template's follow-ups is the organization's setting; the request detail says.",
			tenancy.AreaJobsRequest, writeTool, boundExpiresIn, func(ctx context.Context, c *caller, in runRequestArgs) (any, error) {
				return c.requestResult(ctx, "/job-templates/%s/run-requests", in.TemplateID, in.body())
			}),
		define("request_retry", "Request a retry", "Ask for a retry of a failed run's failed hosts. Nothing runs until a person approves it in Pulsix.",
			tenancy.AreaJobsRequest, writeTool, func(ctx context.Context, c *caller, in retryRequestArgs) (any, error) {
				return c.requestResult(ctx, "/jobs/%s/retry-requests", in.JobID, map[string]any{"reason": in.Reason})
			}),
		define("list_run_requests", "List run requests", "Run requests, newest first; check whether yours was approved.",
			tenancy.AreaJobsRead, readTool, func(ctx context.Context, c *caller, in runRequestListArgs) (any, error) {
				q := url.Values{}
				if in.Status != "" {
					q.Set("status", in.Status)
				}
				return passGet(ctx, c, q, "/run-requests")
			}),
		define("get_run_request", "Get run request", "One run request, what the run would use now, and, once approved, its run group.",
			tenancy.AreaJobsRead, readTool, func(ctx context.Context, c *caller, in runRequestGetArgs) (any, error) {
				return passGet(ctx, c, nil, "/run-requests/%s", in.RequestID)
			}),
		define("cancel_job", "Cancel job", "Cancel a job that one of your approved run requests started.",
			tenancy.AreaJobsRequest, writeTool, func(ctx context.Context, c *caller, in jobArgs) (any, error) {
				return send(ctx, c, http.MethodPost, nil, "", "/jobs/%s/cancel", in.JobID)
			}),
	}
}

// requestResult creates a run or retry request and returns what the
// assistant hands on: the id, the status, the expiry and the link.
func (c *caller) requestResult(ctx context.Context, format, id string, body map[string]any) (any, error) {
	path, err := c.orgPath(format, id)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, http.MethodPost, path, nil, body, "")
	if err != nil {
		return nil, err
	}
	var out struct {
		Request struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			ExpiresAt string `json:"expires_at"`
		} `json:"request"`
		Picture      json.RawMessage `json:"picture"`
		ApprovalPath string          `json:"approval_path"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return map[string]any{
		"request_id": out.Request.ID, "status": out.Request.Status, "expires_at": out.Request.ExpiresAt,
		"approval_url": c.publicURL + out.ApprovalPath,
		"note":         "Nothing runs until a person approves this request at approval_url. Give them the link.",
		"picture":      out.Picture,
	}, nil
}
