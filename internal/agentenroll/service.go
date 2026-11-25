// Package agentenroll decides which hosts a run of a job template with
// agent enrollment enrols the agent on, when a person starts it (spec
// 2026-11-25 assistant API gaps, B; controller ruling C of fix round 1).
//
// Enrollment tokens are minted only for a job that carries an explicit
// list of hosts a signed-in person confirmed: the approval of a run
// request (the picture's list, internal/runrequests) or a manual run or
// retry whose person confirmed the list this package previews. The
// person sees the targets that have no agent now (RunPreview,
// RetryPreview) and sends them back; the start compares what they
// confirmed with the targets without an agent then (ConfirmRun,
// ConfirmRetry) and refuses a difference, so nobody enrols a host they
// did not see. Schedules, chained follow-ups, webhook runs and dry runs
// carry no list and mint nothing.
package agentenroll

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// The services the previews read, each narrowed to what it uses.
type (
	// TemplateSource is a template with its AgentEnrollment flag.
	TemplateSource interface {
		Get(ctx context.Context, orgID, id uuid.UUID) (templates.Template, error)
	}
	// TargetSource is what a run or a retry would target (jobs.Service).
	TargetSource interface {
		LimitedRunTargets(ctx context.Context, orgID, templateID uuid.UUID) ([]uuid.UUID, error)
		RetryTargets(ctx context.Context, orgID, id uuid.UUID) (templateID, runGroupID uuid.UUID, hosts []uuid.UUID, err error)
	}
	// AgentSource names the hosts that run an agent (agents.Service).
	AgentSource interface {
		HostsWithAgent(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	}
	// HostSource names hosts (hosts.Service).
	HostSource interface {
		ListHostsByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]hosts.Host, error)
	}
)

// Errors the transport maps.
var (
	// ErrNotFound is an unknown template.
	ErrNotFound = errors.New("not found")
	// ErrNotAllowed refuses a run of a template with agent enrollment to a
	// caller who may not enrol agents: a person signed in, with
	// agents.write (the REST layer decides).
	ErrNotAllowed = errors.New("this job template enrols agents on the hosts it runs on; starting or retrying its runs needs a person signed in with the agents.write permission")
	// ErrNotEnrolling refuses a confirmed list for a template that does
	// not enrol agents (switched off since the person looked).
	ErrNotEnrolling = errors.New("this job template does not enrol agents (any more); start the run again without enroll_host_ids")
)

// ConfirmError refuses a run of a template with agent enrollment whose
// person did not confirm the hosts it enrols: none was sent (Missing), or
// the list differs from the targets without an agent now (Current).
type ConfirmError struct {
	Missing bool
	Current []Host
}

func (e *ConfirmError) Error() string {
	names := make([]string, len(e.Current))
	for i, h := range e.Current {
		names[i] = h.Name
	}
	list := "no host"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	if e.Missing {
		return "this job template enrols agents on the hosts it runs on; confirm the hosts it enrols now (" + list + ") in enroll_host_ids"
	}
	return "the hosts without an agent changed since you looked; the run would enrol agents on " + list + " now; confirm them again"
}

// Host is a host a run enrols the agent on.
type Host struct {
	ID   uuid.UUID
	Name string
}

// Preview is what a run started now would enrol: Enrols is the template's
// flag; Hosts (never nil) the targets without an agent, by name then id.
type Preview struct {
	Enrols bool
	Hosts  []Host
}

// Service previews and confirms the hosts of a person's run.
type Service struct {
	templates TemplateSource
	targets   TargetSource
	agents    AgentSource
	hosts     HostSource
}

// NewService wires the service.
func NewService(t TemplateSource, targets TargetSource, agents AgentSource, hs HostSource) *Service {
	return &Service{templates: t, targets: targets, agents: agents, hosts: hs}
}

// RunPreview is what a run of templateID started now would enrol, on
// onlyHost alone when given (the template's targets under its limit hold
// it, or the run would be refused).
func (s *Service) RunPreview(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID) (Preview, error) {
	t, err := s.templates.Get(ctx, orgID, templateID)
	if errors.Is(err, templates.ErrNotFound) {
		return Preview{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if err != nil {
		return Preview{}, fmt.Errorf("look up the template: %w", err)
	}
	if !t.AgentEnrollment {
		return Preview{Hosts: []Host{}}, nil
	}
	targets, err := s.targets.LimitedRunTargets(ctx, orgID, templateID)
	if err != nil {
		return Preview{}, fmt.Errorf("resolve the run's targets: %w", err)
	}
	if onlyHost != nil {
		targets = slices.DeleteFunc(targets, func(id uuid.UUID) bool { return id != *onlyHost })
	}
	return s.preview(ctx, orgID, targets)
}

// RetryPreview is what a retry of the job jobID started now would enrol:
// its failed hosts without an agent. An error of the retry's own checks
// (nothing to retry, not finished) is returned as the jobs package's.
func (s *Service) RetryPreview(ctx context.Context, orgID, jobID uuid.UUID) (Preview, error) {
	templateID, _, failed, err := s.targets.RetryTargets(ctx, orgID, jobID)
	if err != nil {
		return Preview{}, err
	}
	t, err := s.templates.Get(ctx, orgID, templateID)
	if errors.Is(err, templates.ErrNotFound) {
		return Preview{}, fmt.Errorf("%w: %w", jobs.ErrNotFound, err)
	}
	if err != nil {
		return Preview{}, fmt.Errorf("look up the template: %w", err)
	}
	if !t.AgentEnrollment {
		return Preview{Hosts: []Host{}}, nil
	}
	return s.preview(ctx, orgID, failed)
}

// ConfirmRun checks the list a person confirmed for a run of templateID
// (nil: none sent) and returns the list the run's jobs store: nil for a
// template without agent enrollment, else the confirmed hosts (never nil).
// may is whether the caller may enrol agents. A template that cannot be
// read as missing passes with no list: the run answers for it.
func (s *Service) ConfirmRun(ctx context.Context, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, confirmed *[]uuid.UUID, may bool) ([]uuid.UUID, error) {
	p, err := s.RunPreview(ctx, orgID, templateID, onlyHost)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return confirm(p, confirmed, may)
}

// ConfirmRetry is ConfirmRun for a retry of the job jobID. A retry its
// own checks refuse passes with no list: the retry answers for it.
func (s *Service) ConfirmRetry(ctx context.Context, orgID, jobID uuid.UUID, confirmed *[]uuid.UUID, may bool) ([]uuid.UUID, error) {
	p, err := s.RetryPreview(ctx, orgID, jobID)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) || errors.Is(err, jobs.ErrNothingToRetry) || errors.Is(err, jobs.ErrNotRunning) ||
			errors.Is(err, jobs.ErrInvalidInput) || errors.Is(err, jobs.ErrDryRunNotRetryable) {
			return nil, nil
		}
		return nil, err
	}
	return confirm(p, confirmed, may)
}

// confirm compares a person's list with the preview, order-free.
func confirm(p Preview, confirmed *[]uuid.UUID, may bool) ([]uuid.UUID, error) {
	if !p.Enrols {
		if confirmed != nil {
			return nil, ErrNotEnrolling
		}
		return nil, nil
	}
	if !may {
		return nil, ErrNotAllowed
	}
	if confirmed == nil {
		return nil, &ConfirmError{Missing: true, Current: p.Hosts}
	}
	current := make([]uuid.UUID, len(p.Hosts))
	for i, h := range p.Hosts {
		current[i] = h.ID
	}
	if !sameSet(current, *confirmed) {
		return nil, &ConfirmError{Current: p.Hosts}
	}
	return current, nil
}

// preview lists targets without an agent, named, by name then id.
func (s *Service) preview(ctx context.Context, orgID uuid.UUID, targets []uuid.UUID) (Preview, error) {
	p := Preview{Enrols: true, Hosts: []Host{}}
	if len(targets) == 0 {
		return p, nil
	}
	with, err := s.agents.HostsWithAgent(ctx, orgID, targets)
	if err != nil {
		return Preview{}, fmt.Errorf("look up the targets' agents: %w", err)
	}
	hs, err := s.hosts.ListHostsByIDs(ctx, orgID, targets)
	if err != nil {
		return Preview{}, fmt.Errorf("list the targets: %w", err)
	}
	for _, h := range hs {
		if !with[h.ID] {
			p.Hosts = append(p.Hosts, Host{ID: h.ID, Name: h.Name})
		}
	}
	slices.SortFunc(p.Hosts, func(a, b Host) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID.String(), b.ID.String()))
	})
	return p, nil
}

// sameSet reports whether a and b hold the same hosts, whatever their
// order or repeats.
func sameSet(a, b []uuid.UUID) bool {
	set := func(in []uuid.UUID) []uuid.UUID {
		out := slices.Clone(in)
		slices.SortFunc(out, func(p, q uuid.UUID) int { return cmp.Compare(p.String(), q.String()) })
		return slices.Compact(out)
	}
	return slices.Equal(set(a), set(b))
}
