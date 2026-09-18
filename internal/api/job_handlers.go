// The run view routes (spec §7 run view, backlog 2.12): the list and
// detail of an organization's runs, the events a run streamed (poll
// fallback), the run/cancel/retry actions and the live SSE stream. Every
// route resolves its scope through requireOrg — reads gated on
// tenancy.PermJobsRead, writes on tenancy.PermJobsRun, the same shape
// every other org-scoped resource in this package uses — and is mounted
// on the bearer-enabled *huma.Group in api.go.
//
// Snapshot safety (the brief's own rule): none of these responses ever
// carries a job's stored payload, a revealed credential or the revealed
// inventory — the claim snapshot exists only at claim time inside the
// mTLS claim response, and the DTOs below simply have no field for any of
// it. The run detail shows the template's NAME only (never its extra
// vars, which is where Task 7's secret masking would otherwise be
// needed), the actor's display name, the worker's name and the sibling
// jobs of the same run group.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// jobDTO is the wire shape of one job, the form the list and every
// action's response carry. It is deliberately the display-minimal half:
// no payload, no stats.
type jobDTO struct {
	ID         string    `json:"id"`
	ZoneID     string    `json:"zone_id"`
	Kind       string    `json:"kind" enum:"run,adhoc,connection_test,bootstrap,rotate_key,revoke_key,sync_repo"`
	Status     string    `json:"status" enum:"queued,claimed,running,cancelling,succeeded,failed,cancelled,error"`
	TemplateID string    `json:"template_id,omitempty"`
	RunGroupID string    `json:"run_group_id"`
	Trigger    string    `json:"trigger" enum:"manual,schedule,webhook,system"`
	TriggerRef string    `json:"trigger_ref,omitempty" doc:"The schedule or webhook trigger that started the job"`
	CreatedAt  time.Time `json:"created_at"`
}

func toJobDTO(j jobs.Job) jobDTO {
	out := jobDTO{
		ID: j.ID.String(), ZoneID: j.ZoneID.String(), Kind: string(j.Kind),
		Status: string(j.Status), RunGroupID: j.RunGroupID.String(),
		Trigger: j.Trigger, CreatedAt: j.CreatedAt,
	}
	if j.TemplateID != nil {
		out.TemplateID = j.TemplateID.String()
	}
	if j.TriggerRef != nil {
		out.TriggerRef = j.TriggerRef.String()
	}
	return out
}

// jobTemplateSummaryDTO names the template a run came from. A deleted
// template (the job's dangling template_id — templates are deleted
// freely and runs outlive them) reads as "(deleted)".
type jobTemplateSummaryDTO struct {
	ID   string `json:"id"`
	Name string `json:"name" doc:"(deleted) when the template no longer exists"`
}

// jobGroupMemberDTO is one sibling of the run group the detail response's
// group field lists, so the UI can show a grouped run.
type jobGroupMemberDTO struct {
	ID     string `json:"id"`
	ZoneID string `json:"zone_id"`
	Status string `json:"status" enum:"queued,claimed,running,cancelling,succeeded,failed,cancelled,error"`
}

// jobDetailDTO is the run view's own record: the job plus everything the
// run view renders — stats, the terminal error, the template summary,
// the actor and worker display names, the targeted host ids and the
// sibling jobs of the same run group. Deliberately nothing about the
// claim payload, the revealed credentials or the revealed inventory: the
// DTO simply has no field for any of it.
type jobDetailDTO struct {
	ID         string                 `json:"id"`
	ZoneID     string                 `json:"zone_id"`
	Kind       string                 `json:"kind" enum:"run,adhoc,connection_test,bootstrap,rotate_key,revoke_key,sync_repo"`
	Status     string                 `json:"status" enum:"queued,claimed,running,cancelling,succeeded,failed,cancelled,error"`
	Template   *jobTemplateSummaryDTO `json:"template"`
	RunGroupID string                 `json:"run_group_id"`
	Trigger    string                 `json:"trigger" enum:"manual,schedule,webhook,system"`
	TriggerRef string                 `json:"trigger_ref,omitempty" doc:"The schedule or webhook trigger that started the job"`
	ActorName  string                 `json:"actor_name,omitempty"`
	WorkerName string                 `json:"worker_name,omitempty"`
	Hosts      []string               `json:"hosts" doc:"The job's target host ids"`
	Stats      map[string]any         `json:"stats"`
	Error      string                 `json:"error,omitempty"`
	Attempts   int                    `json:"attempts" doc:"How often the job was claimed; a requeued job's events carry their attempt"`
	StartedAt  *time.Time             `json:"started_at,omitempty"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
	// Group lists the sibling jobs of the same run group (id/zone/status),
	// so the UI can show a grouped run.
	Group []jobGroupMemberDTO `json:"group"`
}

func toJobDetailDTO(j jobs.Job, group []jobs.Job) jobDetailDTO {
	out := jobDetailDTO{
		ID: j.ID.String(), ZoneID: j.ZoneID.String(), Kind: string(j.Kind),
		Status: string(j.Status), RunGroupID: j.RunGroupID.String(), Trigger: j.Trigger,
		ActorName: j.ActorName, WorkerName: j.WorkerName,
		Stats: j.Stats, Error: j.Error, Attempts: j.Attempts, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
	if j.TriggerRef != nil {
		out.TriggerRef = j.TriggerRef.String()
	}
	out.Hosts = make([]string, len(j.HostIDs))
	for i, h := range j.HostIDs {
		out.Hosts[i] = h.String()
	}
	if j.Stats == nil {
		out.Stats = map[string]any{}
	}
	if j.TemplateID != nil {
		name := j.TemplateName
		if name == "" {
			// A dangling template_id: the template was deleted after the
			// run was created. The run view shows what it was, not a 404.
			name = "(deleted)"
		}
		out.Template = &jobTemplateSummaryDTO{ID: j.TemplateID.String(), Name: name}
	}
	out.Group = make([]jobGroupMemberDTO, 0, len(group))
	for _, g := range group {
		if g.ID == j.ID {
			continue // the detail's own job is not its own sibling
		}
		out.Group = append(out.Group, jobGroupMemberDTO{ID: g.ID.String(), ZoneID: g.ZoneID.String(), Status: string(g.Status)})
	}
	return out
}

type jobListInput struct {
	Org        string `path:"org"`
	Status     string `query:"status"`
	Kind       string `query:"kind"`
	TemplateID string `query:"template_id"`
	Group      string `query:"group"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit"`
}

type jobListOutput struct {
	Body struct {
		Jobs       []jobDTO `json:"jobs"`
		NextCursor string   `json:"next_cursor"`
	}
}

type jobGetInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

type jobDetailOutput struct {
	Body struct {
		Job jobDetailDTO `json:"job"`
	}
}

// jobStartOutput is the 202 body of POST .../run and POST
// .../retry-failed: the jobs the action created (one per involved zone,
// all sharing a run group), so the SPA can navigate to the run view.
type jobStartOutput struct {
	Body struct {
		Jobs []jobDTO `json:"jobs"`
	}
}

type jobCancelOutput struct {
	Body struct {
		Job struct {
			ID     string `json:"id"`
			Status string `json:"status" enum:"queued,claimed,running,cancelling,succeeded,failed,cancelled,error"`
		} `json:"job"`
	}
}

type jobEventsInput struct {
	Org   string `path:"org"`
	ID    string `path:"id"`
	After int64  `query:"after" doc:"Only events with a greater sequence number come back"`
}

type jobEventDTO struct {
	Seq     uint64         `json:"seq" doc:"The event's position in the job's log, strictly increasing across attempts"`
	Attempt int            `json:"attempt" doc:"The claim attempt that produced the event (0 for events recorded before attempts were counted)"`
	At      time.Time      `json:"at"`
	Host    string         `json:"host"`
	Task    string         `json:"task"`
	State   string         `json:"state"`
	Payload map[string]any `json:"payload" doc:"The event payload as stored (a bare stdout line reads as {\"text\": \"...\"})"`
}

func toJobEventDTO(e jobs.Event) jobEventDTO {
	return jobEventDTO{
		Seq: e.Seq, Attempt: e.Attempt, At: e.At, Host: e.Host, Task: e.Task, State: e.State,
		Payload: normalizeEventPayload(e.Payload),
	}
}

// normalizeEventPayload decodes the stored jsonb into a map for the wire
// shape, whatever the at-rest normalization made of it (a JSON object
// passes through, a bare string lands under "text", nothing stays empty).
func normalizeEventPayload(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err == nil {
		return doc
	}
	var scalar any
	if err := json.Unmarshal(raw, &scalar); err == nil {
		return map[string]any{"text": scalar}
	}
	return map[string]any{}
}

type jobEventsOutput struct {
	Body struct {
		Events []jobEventDTO `json:"events"`
	}
}

type jobStreamInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// registerJobRoutes wires the run view's routes under /orgs/{org}/jobs:
// the list (keyset-paginated like hosts), one run's detail (with the
// sibling jobs of its run group), its streamed events, the SSE live
// stream, and the run/cancel/retry actions on job templates and jobs.
func registerJobRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/job-templates/{id}/run", func(ctx context.Context, in *jobGetInput) (*jobStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		created, err := deps.Jobs.EnqueueRun(ctx, scope.OrgID, id, &actor)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("started run of job template %s (%d job(s))", id, len(created)))
		return jobStart(created), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("runJobTemplate", "job_template"))

	huma.Post(hapi, "/api/v1/orgs/{org}/jobs/{id}/cancel", func(ctx context.Context, in *jobGetInput) (*jobCancelOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		if err := deps.Jobs.Cancel(ctx, scope.OrgID, id); err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		// Read back the status the cancel produced, so the response says
		// which of the two shapes happened: cancelled outright (queued)
		// or cancelling (the worker must confirm).
		j, err := deps.Jobs.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("requested cancel of job (now %s)", j.Status))
		out := &jobCancelOutput{}
		out.Body.Job.ID, out.Body.Job.Status = j.ID.String(), string(j.Status)
		return out, nil
	}, withAudit("cancelJob", "job"))

	huma.Post(hapi, "/api/v1/orgs/{org}/jobs/{id}/retry-failed", func(ctx context.Context, in *jobGetInput) (*jobStartOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		actor := scope.UserID
		created, err := deps.Jobs.RetryFailed(ctx, scope.OrgID, id, &actor)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		setAuditObject(ctx, id.String(), fmt.Sprintf("retried %d failed host(s) of job %s", len(created), id))
		return jobStart(created), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("retryFailedJobs", "job"))

	huma.Get(hapi, "/api/v1/orgs/{org}/jobs", func(ctx context.Context, in *jobListInput) (*jobListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		filter, err := parseJobListFilter(in.Status, in.Kind, in.TemplateID, in.Group)
		if err != nil {
			return nil, err
		}
		list, next, err := deps.Jobs.List(ctx, scope.OrgID, filter, in.Cursor, in.Limit)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		out := &jobListOutput{}
		out.Body.Jobs = make([]jobDTO, len(list))
		for i, j := range list {
			out.Body.Jobs[i] = toJobDTO(j)
		}
		out.Body.NextCursor = next
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/jobs/{id}", func(ctx context.Context, in *jobGetInput) (*jobDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		j, err := deps.Jobs.Get(ctx, scope.OrgID, id)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		// The run group's other jobs, so the UI can show a grouped run.
		// The per-zone jobs of one trigger are few; the list endpoint's
		// own clamp bounds the page.
		group, _, err := deps.Jobs.List(ctx, scope.OrgID, jobs.Filter{RunGroupID: &j.RunGroupID}, "", jobs.MaxListLimit)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		out := &jobDetailOutput{}
		out.Body.Job = toJobDetailDTO(j, group)
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/jobs/{id}/events", func(ctx context.Context, in *jobEventsInput) (*jobEventsOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		list, err := deps.Jobs.Events(ctx, scope.OrgID, id, in.After)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		out := &jobEventsOutput{}
		out.Body.Events = make([]jobEventDTO, len(list))
		for i, e := range list {
			out.Body.Events[i] = toJobEventDTO(e)
		}
		return out, nil
	})

	registerJobStreamRoute(hapi, deps)
}

// jobStart shapes an enqueue response from the jobs the action created.
func jobStart(created []jobs.Job) *jobStartOutput {
	out := &jobStartOutput{}
	out.Body.Jobs = make([]jobDTO, len(created))
	for i, j := range created {
		out.Body.Jobs[i] = toJobDTO(j)
	}
	return out
}

// parseJobListFilter turns the list route's string-typed query parameters
// into a jobs.Filter, rejecting an unknown status or kind and a malformed
// template/group/zone id with 400 rather than letting it silently match
// nothing (Task 5's review note: the filter strings are validated
// server-side). Empty strings mean "this filter is not set", the same
// contract parseHostListFilter gives its own optional filters.
func parseJobListFilter(statusStr, kindStr, templateStr, groupStr string) (jobs.Filter, error) {
	var f jobs.Filter
	if statusStr != "" {
		s := jobs.Status(statusStr)
		if !s.Valid() {
			return jobs.Filter{}, huma.Error400BadRequest("invalid status: must be one of queued, claimed, running, cancelling, succeeded, failed, cancelled, error")
		}
		f.Status = &s
	}
	if kindStr != "" {
		k := jobs.Kind(kindStr)
		if !k.Valid() {
			return jobs.Filter{}, huma.Error400BadRequest("invalid kind: must be one of run, adhoc, connection_test, bootstrap, rotate_key, revoke_key, sync_repo")
		}
		f.Kind = &k
	}
	if templateStr != "" {
		id, err := uuid.Parse(templateStr)
		if err != nil {
			return jobs.Filter{}, huma.Error400BadRequest("invalid template_id")
		}
		f.TemplateID = &id
	}
	if groupStr != "" {
		id, err := uuid.Parse(groupStr)
		if err != nil {
			return jobs.Filter{}, huma.Error400BadRequest("invalid group")
		}
		f.RunGroupID = &id
	}
	return f, nil
}

// mapJobsErr converts the jobs package's sentinels to HTTP errors, the
// same shape every other resource's mapper gives its own package's
// sentinels: anything not recognized is logged server-side and replaced
// with a generic 500 rather than ever reaching the client. ErrInvalidCursor
// is the one error a caller can cause via a tampered ?cursor= value — the
// same 400 mapHostsErr gives hosts.ErrInvalidCursor. ErrNothingToRetry and
// ErrNotRunning are action-specific conflicts: a finished run with no
// failed host to re-run, and a job whose state the cancel no longer
// applies to.
func mapJobsErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, jobs.ErrInvalidInput):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, jobs.ErrInvalidCursor):
		return huma.Error400BadRequest("invalid cursor")
	case errors.Is(err, jobs.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, jobs.ErrNotRunning):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, jobs.ErrNothingToRetry):
		return huma.Error422UnprocessableEntity("nothing to retry")
	case errors.Is(err, jobs.ErrEmptyScope):
		return huma.Error422UnprocessableEntity("the template's scope matches no managed host")
	case errors.Is(err, jobs.ErrHostKeyChanged):
		return huma.Error409Conflict(err.Error())
	default:
		slog.ErrorContext(ctx, "unmapped jobs error", "err", err)
		return huma.Error500InternalServerError("internal error")
	}
}
