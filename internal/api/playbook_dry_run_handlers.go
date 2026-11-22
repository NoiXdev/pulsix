package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// playbookDryRunBody is POST .../playbooks/{id}/dry-run (spec 2026-11-22
// §1): a run of template_id in Ansible's check mode.
type playbookDryRunBody struct {
	TemplateID string   `json:"template_id" doc:"A template whose playbook is this one"`
	HostIDs    []string `json:"host_ids,omitempty" doc:"Narrow the run to these of the template's target hosts; empty runs on all of them"`
	Tags       []string `json:"tags,omitempty" maxItems:"32" doc:"Run only the tasks with these tags; each 1 to 64 letters, digits or _ . : -"`
	Diff       *bool    `json:"diff,omitempty" doc:"Show the differences a task would make; true when omitted"`
	Draft      *string  `json:"draft,omitempty" doc:"An editor playbook's unsaved text, checked instead of the saved one; needs playbooks.write"`
}

type playbookDryRunInput struct {
	Org  string `path:"org"`
	ID   string `path:"id"`
	Body playbookDryRunBody
}

// registerPlaybookDryRunRoutes wires the dry run of a playbook from its
// page: an ordinary manual run of one of its templates, marked check mode,
// with no follow-ups.
func registerPlaybookDryRunRoutes(hapi huma.API, deps Deps) {
	huma.Post(hapi, "/api/v1/orgs/{org}/playbooks/{id}/dry-run", func(ctx context.Context, in *playbookDryRunInput) (*jobStartOutput, error) {
		scope, err := authorizeDryRun(func(p tenancy.Permission) (Scope, error) {
			return requireOrg(ctx, deps, in.Org, p)
		}, in.Body.Draft != nil)
		if err != nil {
			return nil, err
		}
		playbookID, err := parseUUID(in.ID, "playbook id")
		if err != nil {
			return nil, err
		}
		if in.Body.Draft != nil {
			if err := playbooks.ValidateYAML(*in.Body.Draft); err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error(), &huma.ErrorDetail{Location: "body.draft", Message: err.Error()})
			}
		}
		templateID, req, err := dryRunRequest(playbookID, scope.UserID, in.Body)
		if err != nil {
			return nil, err
		}
		created, err := deps.Jobs.EnqueueRunWith(ctx, scope.OrgID, templateID, req)
		if err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		auditDryRun(ctx, playbookID, templateID, req.Overrides, created)
		return jobStart(created), nil
	}, func(o *huma.Operation) { o.DefaultStatus = http.StatusAccepted }, withAudit("dryRunPlaybook", "playbook"))
}

// authorizeDryRun asks check for jobs.run, as running the template does,
// and with a draft also for playbooks.write: a draft is an edit that has
// not been saved, and must not run with less permission than saving it.
func authorizeDryRun(check func(tenancy.Permission) (Scope, error), draft bool) (Scope, error) {
	scope, err := check(tenancy.PermJobsRun)
	if err != nil {
		return Scope{}, err
	}
	if draft {
		if _, err := check(tenancy.PermPlaybooksWrite); err != nil {
			return Scope{}, err
		}
	}
	return scope, nil
}

// dryRunRequest turns the body into the run request: manual, check mode,
// diff unless turned off, the template bound to the playbook. The rules on
// tags, drafts and hosts are the jobs service's.
func dryRunRequest(playbookID, actor uuid.UUID, b playbookDryRunBody) (uuid.UUID, jobs.RunRequest, error) {
	templateID, err := parseUUID(b.TemplateID, "template_id")
	if err != nil {
		return uuid.Nil, jobs.RunRequest{}, err
	}
	var hosts []uuid.UUID
	for _, raw := range b.HostIDs {
		id, err := parseUUID(raw, "host_ids")
		if err != nil {
			return uuid.Nil, jobs.RunRequest{}, err
		}
		hosts = append(hosts, id)
	}
	ov := &jobs.RunOverrides{Check: true, Diff: b.Diff == nil || *b.Diff, Tags: b.Tags, Playbook: b.Draft}
	return templateID, jobs.RunRequest{
		Trigger: jobs.TriggerManual, Actor: &actor, Overrides: ov, OnlyHosts: hosts, PlaybookID: &playbookID,
	}, nil
}

// auditDryRun records the dry run like a template run, plus what made it
// one: the tags, the diff flag, how many hosts it covers and, for a draft,
// the draft's SHA-256 — never its text.
func auditDryRun(ctx context.Context, playbookID, templateID uuid.UUID, ov *jobs.RunOverrides, created []jobs.Job) {
	hosts := 0
	for _, j := range created {
		hosts += len(j.HostIDs)
	}
	setAuditObject(ctx, playbookID.String(),
		fmt.Sprintf("started dry run of playbook %s with job template %s (%d job(s), %d host(s))", playbookID, templateID, len(created), hosts))
	tags := ov.Tags
	if tags == nil {
		tags = []string{}
	}
	setAuditMeta(ctx, "check_mode", true)
	setAuditMeta(ctx, "diff", ov.Diff)
	setAuditMeta(ctx, "tags", tags)
	setAuditMeta(ctx, "host_count", hosts)
	if ov.Playbook != nil {
		sum := sha256.Sum256([]byte(*ov.Playbook))
		setAuditMeta(ctx, "draft_sha256", hex.EncodeToString(sum[:]))
	}
}

// dryRunDTO is what a dry run recorded about itself: its tags, whether
// differences were shown and whether it checked an unsaved draft (never
// the draft itself).
type dryRunDTO struct {
	Tags  []string `json:"tags" nullable:"false" doc:"The tags the run was limited to; empty for all tasks"`
	Diff  bool     `json:"diff" doc:"Whether the run reported differences"`
	Draft bool     `json:"draft" doc:"Whether the run checked an unsaved draft instead of the saved playbook"`
}

func toDryRunDTO(d *jobs.DryRun) *dryRunDTO {
	if d == nil {
		return nil
	}
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	return &dryRunDTO{Tags: tags, Diff: d.Diff, Draft: d.Draft}
}
