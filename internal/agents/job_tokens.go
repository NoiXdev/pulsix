// The enrollment tokens an enrolling run hands its hosts (spec 2026-11-25
// assistant API gaps, B): the claim of a job with a confirmed enroll list
// mints one host-bound single-use token per listed host without an agent;
// the job's re-claim, requeue and terminal paths revoke the unused ones.
package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/hosts"
)

// JobTokenTarget is one host a run may enrol: its id and inventory name.
type JobTokenTarget struct {
	HostID uuid.UUID
	Name   string
}

// JobTokensInput is what MintJobTokens mints for: the run, its zone, the
// tokens' expiry and the hosts it enrols.
type JobTokensInput struct {
	ZoneID    uuid.UUID
	JobID     uuid.UUID
	ExpiresAt time.Time
	Targets   []JobTokenTarget
}

// JobToken is one minted token: the host it is bound to and the printed
// agent token (AgentString). The token is a secret: it travels only in the
// claim's inventory and redact list, never into a log, a record or a
// response.
type JobToken struct {
	HostID uuid.UUID
	Name   string
	Token  string
}

// MintJobTokens mints, in target order, one host-bound single-use token
// per target that runs no agent, linked to the job. Run it on a write
// transaction (db.WithWriteTx): a failure part-way leaves no token behind
// once the caller rolls back. A target's name is the token's name hint
// when it is a valid one (the token is bound, so the hint only labels it).
func (s *Service) MintJobTokens(ctx context.Context, orgID uuid.UUID, in JobTokensInput) ([]JobToken, error) {
	if len(in.Targets) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(in.Targets))
	for i, t := range in.Targets {
		ids[i] = t.HostID
	}
	has, err := s.HostsWithAgent(ctx, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("hosts with an agent: %w", err)
	}
	jobID := in.JobID
	var out []JobToken
	for _, t := range in.Targets {
		if has[t.HostID] {
			continue
		}
		hostID := t.HostID
		tok, err := s.createToken(ctx, orgID, TokenInput{
			ZoneID: in.ZoneID, HostID: &hostID, NameHint: jobTokenHint(t.Name), MaxUses: 1, ExpiresAt: in.ExpiresAt,
		}, &jobID)
		if err != nil {
			return nil, fmt.Errorf("mint the token of host %s: %w", t.Name, err)
		}
		out = append(out, JobToken{HostID: t.HostID, Name: t.Name, Token: enrolltoken.AgentString(tok)})
	}
	return out, nil
}

// jobTokenHint is name when it is a valid token name hint, else "".
func jobTokenHint(name string) string {
	if !hosts.ValidName(name) || ca.CheckAgentName(name) != nil {
		return ""
	}
	return name
}

// RevokeJobTokens deletes the unused tokens minted for jobID and reports
// how many; a used one is already consumed by its claim and stays.
func (s *Service) RevokeJobTokens(ctx context.Context, orgID, jobID uuid.UUID) (int, error) {
	return s.store.RevokeJobTokens(ctx, orgID, jobID)
}
