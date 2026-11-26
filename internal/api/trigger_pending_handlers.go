package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/triggers"
)

type triggerPendingOutput struct {
	Body struct {
		Pending *pendingRunDTO `json:"pending,omitempty" doc:"The trigger's collected run while it waits; absent when none waits"`
	}
}

type triggerRunPendingOutput struct {
	Body struct {
		Outcome string `json:"outcome" enum:"triggered,failed,disabled" doc:"triggered: the run started (run_id); failed: it could not start (its deliveries say why); disabled: the trigger is disabled, the collected run was dropped"`
		RunID   string `json:"run_id,omitempty" doc:"The started run's first job"`
	}
}

// registerTriggerPendingRoutes wires a settling trigger's collected run
// (backlog 2.137): reading it (the trigger's read permission), starting it
// now as the person (the test call's permission: it starts what the
// trigger starts) and discarding it (which only stops a run, like
// disabling the trigger). Neither waits for a quiet moment or an active
// run; a call that finds nothing waiting is 409.
func registerTriggerPendingRoutes(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/triggers/{id}/pending", func(ctx context.Context, in *triggerIDInput) (*triggerPendingOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, triggerRead)
		if err != nil {
			return nil, err
		}
		p, err := deps.Triggers.Pending(ctx, scope.OrgID, t.ID)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		out := &triggerPendingOutput{}
		if p != nil {
			out.Body.Pending = toPendingRunDTO(*p)
		}
		return out, nil
	})

	huma.Post(hapi, "/api/v1/orgs/{org}/triggers/{id}/pending/run", func(ctx context.Context, in *triggerIDInput) (*triggerRunPendingOutput, error) {
		scope, t, err := loadTriggerToRun(ctx, deps, in.Org, in.ID)
		if err != nil {
			return nil, err
		}
		res, err := deps.Triggers.RunPending(ctx, scope.OrgID, t.ID, scope.UserID)
		if err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "started the collected run of webhook trigger "+t.Name+": "+string(res.Outcome))
		out := &triggerRunPendingOutput{}
		out.Body.Outcome = string(res.Outcome)
		if res.Outcome == triggers.OutcomeTriggered && res.RunID != nil {
			out.Body.RunID = res.RunID.String()
		}
		return out, nil
	}, withAudit("runPendingTrigger", "trigger"))

	huma.Delete(hapi, "/api/v1/orgs/{org}/triggers/{id}/pending", func(ctx context.Context, in *triggerIDInput) (*okOutput, error) {
		scope, t, err := loadTrigger(ctx, deps, in.Org, in.ID, triggerStop)
		if err != nil {
			return nil, err
		}
		if err := deps.Triggers.DiscardPending(ctx, scope.OrgID, t.ID); err != nil {
			return nil, mapTriggersErr(ctx, err)
		}
		setAuditObject(ctx, t.ID.String(), "discarded the collected run of webhook trigger "+t.Name)
		return &okOutput{}, nil
	}, withAudit("discardPendingTrigger", "trigger"))
}
