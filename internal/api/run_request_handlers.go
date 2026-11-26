// Run request routes (spec docs/superpowers/specs/2026-11-16-assistant-
// access-design.md, "Run requests"): an assistant asks for a run or a retry
// (jobs.request); a person approves or rejects it in an interactive session
// (jobs.run), sending back the fingerprint of the run picture they saw.
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type runRequestDTO struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind" enum:"run,retry_failed,schedule,trigger,schedule_review,trigger_review" doc:"run, retry_failed, schedule (create, change or resume a schedule; the picture's schedule says which), trigger (create, change or give a new secret to a webhook trigger; the picture's trigger says which), or schedule_review / trigger_review (an assistant's change paused a schedule or disabled a trigger; approving resumes or enables it, the picture's review shows the change)"`
	Status             string `json:"status" enum:"pending,approved,rejected,expired,waiting_for_sync,failed"`
	TemplateID         string `json:"template_id" doc:"The template the request runs; empty for a repository sync trigger's request, which runs none"`
	TemplateRevision   int64  `json:"template_revision" doc:"The template's revision when the request was made; the picture's template_revision is the one an approval approves"`
	HostID             string `json:"host_id,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	Reason             string `json:"reason"`
	RequestedByUserID  string `json:"requested_by_user_id"`
	RequestedByTokenID string `json:"requested_by_token_id,omitempty"`
	// RequestedByTokenName and TokenState describe the requesting token as
	// it is now: a hint for the approver, never a block.
	RequestedByTokenName string     `json:"requested_by_token_name" doc:"The requesting token's name; empty when the token is gone or there is none"`
	TokenState           string     `json:"token_state,omitempty" enum:"active,revoked,expired,deleted" doc:"Whether the requesting token can still ask for runs; approving starts the run either way"`
	DecidedByUserID      string     `json:"decided_by_user_id,omitempty"`
	DecidedAt            *time.Time `json:"decided_at,omitempty"`
	RunGroupID           string     `json:"run_group_id,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty" doc:"When a pending request expires; absent while it waits for its sync, whose success starts the expiry"`
	SyncJobID            string     `json:"sync_job_id,omitempty" doc:"The repository sync a sync_first request waits or waited for"`
	SyncError            string     `json:"sync_error,omitempty" doc:"Why a failed request's sync did not let it become pending"`
	TargetID             string     `json:"target_id,omitempty" doc:"A schedule or trigger request's schedule or trigger: the one it creates (its id is taken when the request is made), changes, resumes or rotates"`
	CreatedAt            time.Time  `json:"created_at"`
}

func toRunRequestDTO(r runrequests.Request) runRequestDTO {
	dto := runRequestDTO{
		ID: r.ID.String(), Kind: string(r.Kind), Status: string(r.Status), TemplateRevision: r.TemplateRevision,
		Reason: r.Reason, RequestedByUserID: r.RequestedByUser.String(), DecidedAt: r.DecidedAt,
		CreatedAt: r.CreatedAt, SyncError: r.SyncError,
		RequestedByTokenName: r.TokenName, TokenState: string(r.TokenState),
	}
	if !r.ExpiresAt.IsZero() {
		at := r.ExpiresAt
		dto.ExpiresAt = &at
	}
	dto.SyncJobID = uuidString(r.SyncJobID)
	dto.HostID, dto.JobID, dto.RequestedByTokenID = uuidString(r.HostID), uuidString(r.JobID), uuidString(r.RequestedByToken)
	dto.DecidedByUserID, dto.RunGroupID = uuidString(r.DecidedBy), uuidString(r.RunGroupID)
	dto.TargetID = uuidString(r.TargetID)
	if r.TemplateID != uuid.Nil {
		dto.TemplateID = r.TemplateID.String()
	}
	return dto
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

type runRequestDetailOutput struct {
	Body struct {
		Request      runRequestDTO       `json:"request"`
		Picture      runrequests.Picture `json:"picture" doc:"What the run would use if it were approved now; for a schedule request, the schedule as the approval stores it (schedule) and its template"`
		Fingerprint  string              `json:"fingerprint" doc:"Send it back on approve; a change to the picture refuses the approval"`
		ApprovalPath string              `json:"approval_path" doc:"The GUI page where a person approves or rejects the request"`
	}
}

type runRequestOutput struct {
	Body struct {
		Request runRequestDTO `json:"request"`
	}
}

// runRequestApproveOutput is the approval's answer. After a trigger
// request that creates a trigger or gives it a new secret it carries that
// secret, once, to the approving person: no cache may keep the response,
// and nothing else ever returns the secret again.
type runRequestApproveOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Request       runRequestDTO `json:"request"`
		TriggerSecret string        `json:"trigger_secret,omitempty" doc:"The webhook trigger's new secret after a trigger request that creates a trigger or rotates its secret: shown once, to the approving person only; store it in the sender"`
		TriggerURL    string        `json:"trigger_url,omitempty" doc:"Where the trigger's senders post to (a trigger request)"`
	}
}

type runRequestListOutput struct {
	Body struct {
		Requests []runRequestDTO `json:"requests"`
	}
}

type runRequestPendingCountOutput struct {
	Body struct {
		Count int `json:"count" doc:"Pending run requests that have not expired"`
	}
}

type runRequestIDInput struct {
	Org string `path:"org"`
	ID  string `path:"id"`
}

// runRequestWaitInput is GET …/run-requests/{id}, which can wait for a
// status change.
type runRequestWaitInput struct {
	Org    string `path:"org"`
	ID     string `path:"id"`
	Wait   int    `query:"wait" minimum:"0" maximum:"25" default:"0" doc:"Seconds to wait (0 to 25, default 0) for the status to change before answering; the answer is the current picture either way"`
	Unless string `query:"unless" maxLength:"40" doc:"The status the caller already knows: the wait ends once the status differs from it. Without it, the status at the first read is the one waited out"`
}

// registerRunRequestRoutes wires the run request routes.
func registerRunRequestRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/job-templates/{id}/run-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body requestRunRequest
	}) (*runRequestDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRequest)
		if err != nil {
			return nil, err
		}
		templateID, err := parseUUID(in.ID, "template id")
		if err != nil {
			return nil, err
		}
		create, err := in.Body.input()
		if err != nil {
			return nil, err
		}
		if create.SyncFirst {
			// Syncing first starts a sync of the repository, which the
			// sync route allows only with repos.write.
			if _, err := requireOrg(ctx, deps, in.Org, tenancy.PermReposWrite); err != nil {
				return nil, err
			}
		}
		create.TemplateID, create.RequestedByUser, create.RequestedByToken = templateID, scope.UserID, scope.TokenID
		r, err := deps.RunRequests.Create(ctx, scope.OrgID, create)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), requestSummary(r))
		auditSyncFirst(ctx, deps, scope, r)
		return runRequestDetail(ctx, deps, scope, r.ID)
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("requestRun", "run_request"))

	huma.Post(hapi, "/api/v1/orgs/{org}/jobs/{id}/retry-requests", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body struct {
			Reason string `json:"reason" maxLength:"300"`
		}
	}) (*runRequestDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRequest)
		if err != nil {
			return nil, err
		}
		jobID, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		r, err := deps.RunRequests.Create(ctx, scope.OrgID, runrequests.CreateInput{
			Kind: runrequests.KindRetryFailed, JobID: &jobID, Reason: in.Body.Reason,
			RequestedByUser: scope.UserID, RequestedByToken: scope.TokenID,
		})
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "requested a retry of job "+jobID.String()+": "+r.Reason)
		return runRequestDetail(ctx, deps, scope, r.ID)
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusCreated }, withAudit("requestRetry", "run_request"))

	huma.Get(hapi, "/api/v1/orgs/{org}/run-requests", func(ctx context.Context, in *struct {
		Org    string `path:"org"`
		Status string `query:"status" enum:"pending,approved,rejected,expired,waiting_for_sync,failed"`
	}) (*runRequestListOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		var status *runrequests.Status
		if in.Status != "" {
			st := runrequests.Status(in.Status)
			status = &st
		}
		rows, err := deps.RunRequests.List(ctx, scope.OrgID, status)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		out := &runRequestListOutput{}
		out.Body.Requests = make([]runRequestDTO, len(rows))
		for i, r := range rows {
			out.Body.Requests[i] = toRunRequestDTO(r)
		}
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/run-requests/pending-count", func(ctx context.Context, in *struct {
		Org string `path:"org"`
	}) (*runRequestPendingCountOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		n, err := deps.RunRequests.PendingCount(ctx, scope.OrgID)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		out := &runRequestPendingCountOutput{}
		out.Body.Count = n
		return out, nil
	})

	huma.Get(hapi, "/api/v1/orgs/{org}/run-requests/{id}", func(ctx context.Context, in *runRequestWaitInput) (*runRequestDetailOutput, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "run request id")
		if err != nil {
			return nil, err
		}
		// Each poll reads the status only; the picture is resolved once,
		// after the wait.
		if in.Wait > 0 {
			if _, err := waitForChange(ctx, deps, in.Wait, in.Unless, func() (struct{}, string, error) {
				st, err := deps.RunRequests.Status(ctx, scope.OrgID, id)
				return struct{}{}, string(st), err
			}); err != nil {
				return nil, mapRunRequestErr(ctx, err)
			}
		}
		return runRequestDetail(ctx, deps, scope, id)
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/run-requests/{id}/approve", func(ctx context.Context, in *struct {
		Org  string `path:"org"`
		ID   string `path:"id"`
		Body struct {
			Fingerprint string `json:"fingerprint" minLength:"1" doc:"The fingerprint of the detail the person reviewed"`
			// FollowUps is the person's selection: omitted takes the
			// default, an empty list starts none.
			FollowUps *[]string `json:"follow_ups,omitempty" maxItems:"10" doc:"Direct follow-ups to start, a subset of the picture's; omitted starts all of them when the organization allows follow-ups"`
		}
	}) (*runRequestApproveOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "run request id")
		if err != nil {
			return nil, err
		}
		var selection *[]uuid.UUID
		if in.Body.FollowUps != nil {
			ids := make([]uuid.UUID, len(*in.Body.FollowUps))
			for i, raw := range *in.Body.FollowUps {
				if ids[i], err = parseUUID(raw, "follow_ups"); err != nil {
					return nil, err
				}
			}
			selection = &ids
		}
		// Approving a schedule or trigger request also needs what their
		// routes need (templates.write, or repos.write for a repository
		// sync trigger); the service asks for it by kind. Every approval
		// needs jobs.run (above): it is the approvals page's permission,
		// and every role that writes repositories holds it.
		may := runrequests.Approver{Enrol: mayEnrolAgents(scope), WriteTemplates: tenancy.Allows(scope.Role, tenancy.PermTemplatesWrite),
			WriteRepos: tenancy.Allows(scope.Role, tenancy.PermReposWrite)}
		a, err := deps.RunRequests.ApproveWith(ctx, scope.OrgID, id, scope.UserID, in.Body.Fingerprint, selection, may)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		switch {
		case a.Schedule != nil:
			setAuditObject(ctx, a.Request.ID.String(), scheduleApprovalSummary(a))
			auditAppliedSchedule(ctx, deps, scope, a)
		case a.Trigger != nil:
			setAuditObject(ctx, a.Request.ID.String(), triggerApprovalSummary(a))
			auditAppliedTrigger(ctx, deps, scope, a)
		case a.Review != nil:
			setAuditObject(ctx, a.Request.ID.String(), reviewApprovalSummary(a))
			auditAppliedReview(ctx, deps, scope, a)
		default:
			setAuditObject(ctx, a.Request.ID.String(), approvalSummary(a))
		}
		out := &runRequestApproveOutput{CacheControl: "no-store"}
		out.Body.Request = toRunRequestDTO(a.Request)
		if a.Trigger != nil {
			out.Body.TriggerSecret, out.Body.TriggerURL = a.TriggerSecret, a.Trigger.URL
		}
		return out, nil
	}, withAudit("approveRunRequest", "run_request"))

	huma.Post(hapi, "/api/v1/orgs/{org}/run-requests/{id}/reject", func(ctx context.Context, in *runRequestIDInput) (*runRequestOutput, error) {
		if _, err := requireInteractiveSession(ctx); err != nil {
			return nil, err
		}
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRun)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "run request id")
		if err != nil {
			return nil, err
		}
		r, err := deps.RunRequests.Reject(ctx, scope.OrgID, id, scope.UserID)
		if err != nil {
			return nil, mapRunRequestErr(ctx, err)
		}
		setAuditObject(ctx, r.ID.String(), "rejected run request")
		out := &runRequestOutput{}
		out.Body.Request = toRunRequestDTO(r)
		return out, nil
	}, withAudit("rejectRunRequest", "run_request"))
}

// approvalSummary is the audit summary of an approval: the template
// revision the person approved, the commit the run is pinned to, how many
// hosts it enrols agents on, a dry run and the overrides by field (no
// values, see overrideFields) and, when the template has follow-ups, the
// ones that start.
func approvalSummary(a runrequests.Approved) string {
	summary := "approved run request (template revision " + strconv.FormatInt(a.TemplateRevision, 10)
	if a.Request.PinnedCommit != "" {
		summary += "; commit " + a.Request.PinnedCommit
	}
	if a.Request.EnrollHostIDs != nil {
		summary += "; enrols agents on " + strconv.Itoa(len(a.Request.EnrollHostIDs)) + " host(s)"
	}
	if ov := a.Request.Overrides; ov != nil {
		if ov.Check {
			summary += "; dry run"
		}
		if fields := overrideFields(ov); fields != "" {
			summary += "; overrides: " + fields
		}
	}
	if a.FollowUps != nil {
		if len(a.Started) == 0 {
			summary += "; no follow-ups"
		} else {
			names := make([]string, len(a.Started))
			for i, n := range a.Started {
				names[i] = n.TemplateName
			}
			summary += "; follow-ups: " + strings.Join(names, ", ")
		}
	}
	return summary + ")"
}

// auditSyncFirst records the sync a sync_first request queued, or the one
// it attached to, in the repository's feed as a sync_repo call would be,
// attributed to the requester and its token.
func auditSyncFirst(ctx context.Context, deps Deps, scope Scope, r runrequests.Request) {
	st := r.SyncStarted
	if st == nil {
		return
	}
	summary := "requested sync of repo (sync first for run request " + r.ID.String() + ")"
	if st.AlreadyQueued {
		summary += "; sync job " + st.JobID.String() + " was already queued"
	}
	recordOrgEvent(ctx, deps, scope.OrgID, scope.UserID, http.MethodPost+" syncRepo", "repo", st.RepoID.String(), summary)
}

func runRequestDetail(ctx context.Context, deps Deps, scope Scope, id uuid.UUID) (*runRequestDetailOutput, error) {
	r, picture, fingerprint, err := deps.RunRequests.Get(ctx, scope.OrgID, id)
	if err != nil {
		return nil, mapRunRequestErr(ctx, err)
	}
	out := &runRequestDetailOutput{}
	out.Body.Request, out.Body.Picture, out.Body.Fingerprint = toRunRequestDTO(r), picture, fingerprint
	out.Body.ApprovalPath = "/o/" + scope.Org.Slug + "/runs/requests/" + r.ID.String()
	return out, nil
}

func mapRunRequestErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, runrequests.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, runrequests.ErrEnrollmentNotAllowed), errors.Is(err, runrequests.ErrNotAllowed):
		return huma.Error403Forbidden(err.Error())
	case errors.Is(err, runrequests.ErrInvalidInput):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, runrequests.ErrTooManyPending):
		return huma.Error429TooManyRequests(err.Error())
	case errors.Is(err, runrequests.ErrExpired), errors.Is(err, runrequests.ErrDecided), errors.Is(err, runrequests.ErrChanged),
		errors.Is(err, runrequests.ErrWaitingForSync), errors.Is(err, runrequests.ErrSyncFailed):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, runrequests.ErrCommitBlocked):
		// The run's commit cannot be pinned yet: the repository must be
		// synced first, or a sync of it is running. The message says
		// which; it names no URL.
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, jobs.ErrNoZone):
		// sync_first queues a sync, which runs in the default zone; the
		// sync route answers the same.
		return huma.Error409Conflict("the organization has no default zone, so nothing can be synced yet")
	case errors.Is(err, inventory.ErrUnrenderable):
		// The run picture renders the targets' inventory as the claim does,
		// which fails closed (a stray sealed value, a broken jump host
		// chain). The message names the group or host and the reason; the
		// renderer never puts a value, let alone a secret, into it.
		return huma.Error409Conflict(err.Error())
	}
	// Anything else came from the jobs paths an approval enqueues through.
	return mapJobsErr(ctx, err)
}
