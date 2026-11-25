// The enrollment port adapter: the agent registry behind the job queue's
// EnrollmentTokens port (spec 2026-11-25 assistant API gaps, B). The queue
// mints the tokens of an enrolling run at its claim and revokes the unused
// ones when the job is requeued or ends; internal/jobs does not import
// internal/agents, so the conversion lives here, in the wiring.
package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/jobs"
)

// jobEnrollmentTokens adapts agents.Service to jobs.EnrollmentTokens.
type jobEnrollmentTokens struct{ agents *agents.Service }

// NewJobEnrollmentTokens builds the adapter both wirings share (app.Build
// and apitest.New, which mirrors it).
func NewJobEnrollmentTokens(a *agents.Service) jobs.EnrollmentTokens {
	return jobEnrollmentTokens{agents: a}
}

// MintJobTokens implements jobs.EnrollmentTokens.
func (a jobEnrollmentTokens) MintJobTokens(ctx context.Context, orgID, zoneID, jobID uuid.UUID, targets []jobs.EnrollTarget, expiresAt time.Time) ([]jobs.EnrollToken, error) {
	in := agents.JobTokensInput{ZoneID: zoneID, JobID: jobID, ExpiresAt: expiresAt, Targets: make([]agents.JobTokenTarget, len(targets))}
	for i, t := range targets {
		in.Targets[i] = agents.JobTokenTarget{HostID: t.HostID, Name: t.Name}
	}
	minted, err := a.agents.MintJobTokens(ctx, orgID, in)
	if err != nil {
		return nil, err
	}
	out := make([]jobs.EnrollToken, len(minted))
	for i, m := range minted {
		out[i] = jobs.EnrollToken{HostID: m.HostID, Name: m.Name, Token: m.Token}
	}
	return out, nil
}

// RevokeJobTokens implements jobs.EnrollmentTokens.
func (a jobEnrollmentTokens) RevokeJobTokens(ctx context.Context, orgID, jobID uuid.UUID) (int, error) {
	return a.agents.RevokeJobTokens(ctx, orgID, jobID)
}
