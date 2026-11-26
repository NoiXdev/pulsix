package agentenroll_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agentenroll"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

var errBoom = errors.New("boom")

type errTemplates struct {
	t   templates.Template
	err error
}

func (f errTemplates) Get(context.Context, uuid.UUID, uuid.UUID) (templates.Template, error) {
	return f.t, f.err
}

type errTargets struct {
	targets []uuid.UUID
	err     error
	retryEr error
}

func (f errTargets) LimitedRunTargets(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return f.targets, f.err
}

func (f errTargets) RetryTargets(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, uuid.UUID, []uuid.UUID, error) {
	return uuid.New(), uuid.Nil, f.targets, f.retryEr
}

type errAgents struct{ err error }

func (f errAgents) HostsWithAgent(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return nil, f.err
}

type errHosts struct{ err error }

func (f errHosts) ListHostsByIDs(context.Context, uuid.UUID, []uuid.UUID) ([]hosts.Host, error) {
	return nil, f.err
}

func enrolling() errTemplates {
	return errTemplates{t: templates.Template{AgentEnrollment: true}}
}

func TestConfirmErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		err  agentenroll.ConfirmError
		want []string
	}{
		{"missing with hosts", agentenroll.ConfirmError{Missing: true, Current: []agentenroll.Host{{Name: "a"}, {Name: "b"}}}, []string{"confirm the hosts", "a, b"}},
		{"missing without hosts", agentenroll.ConfirmError{Missing: true}, []string{"no host"}},
		{"changed", agentenroll.ConfirmError{Current: []agentenroll.Host{{Name: "web-2"}}}, []string{"changed since you looked", "web-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("%q lacks %q", msg, w)
				}
			}
		})
	}
}

func TestRunPreviewErrors(t *testing.T) {
	org, tpl := uuid.New(), uuid.New()
	cases := []struct {
		name  string
		svc   *agentenroll.Service
		is    error
		wraps error
	}{
		{"unknown template", agentenroll.NewService(errTemplates{err: templates.ErrNotFound}, errTargets{}, errAgents{}, errHosts{}), agentenroll.ErrNotFound, templates.ErrNotFound},
		{"template lookup fails", agentenroll.NewService(errTemplates{err: errBoom}, errTargets{}, errAgents{}, errHosts{}), errBoom, nil},
		{"targets fail", agentenroll.NewService(enrolling(), errTargets{err: errBoom}, errAgents{}, errHosts{}), errBoom, nil},
		{"agents lookup fails", agentenroll.NewService(enrolling(), errTargets{targets: []uuid.UUID{uuid.New()}}, errAgents{err: errBoom}, errHosts{}), errBoom, nil},
		{"host list fails", agentenroll.NewService(enrolling(), errTargets{targets: []uuid.UUID{uuid.New()}}, errAgents{}, errHosts{err: errBoom}), errBoom, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.svc.RunPreview(ctx, org, tpl, nil)
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
			if tc.wraps != nil && !errors.Is(err, tc.wraps) {
				t.Fatalf("err = %v does not wrap %v", err, tc.wraps)
			}
		})
	}
}

func TestRunPreviewNoTargetsIsEmptyNotNil(t *testing.T) {
	svc := agentenroll.NewService(enrolling(), errTargets{}, errAgents{err: errBoom}, errHosts{err: errBoom})
	p, err := svc.RunPreview(ctx, uuid.New(), uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enrols || p.Hosts == nil || len(p.Hosts) != 0 {
		t.Fatalf("preview = %+v", p)
	}
}

func TestRetryPreviewErrors(t *testing.T) {
	org, job := uuid.New(), uuid.New()
	t.Run("retry's own error passes through", func(t *testing.T) {
		svc := agentenroll.NewService(enrolling(), errTargets{retryEr: jobs.ErrNothingToRetry}, errAgents{}, errHosts{})
		if _, err := svc.RetryPreview(ctx, org, job); !errors.Is(err, jobs.ErrNothingToRetry) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown template is a jobs not found", func(t *testing.T) {
		svc := agentenroll.NewService(errTemplates{err: templates.ErrNotFound}, errTargets{}, errAgents{}, errHosts{})
		if _, err := svc.RetryPreview(ctx, org, job); !errors.Is(err, jobs.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("template lookup fails", func(t *testing.T) {
		svc := agentenroll.NewService(errTemplates{err: errBoom}, errTargets{}, errAgents{}, errHosts{})
		if _, err := svc.RetryPreview(ctx, org, job); !errors.Is(err, errBoom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("template without enrollment previews nothing", func(t *testing.T) {
		svc := agentenroll.NewService(errTemplates{}, errTargets{}, errAgents{err: errBoom}, errHosts{})
		p, err := svc.RetryPreview(ctx, org, job)
		if err != nil || p.Enrols || p.Hosts == nil {
			t.Fatalf("preview = %+v, err = %v", p, err)
		}
	})
}

func TestConfirmPropagatesUnexpectedErrors(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	run := agentenroll.NewService(errTemplates{err: errBoom}, errTargets{}, errAgents{}, errHosts{})
	if _, err := run.ConfirmRun(ctx, org, id, nil, nil, true); !errors.Is(err, errBoom) {
		t.Fatalf("ConfirmRun err = %v", err)
	}
	if _, err := run.ConfirmRetry(ctx, org, id, nil, true); !errors.Is(err, errBoom) {
		t.Fatalf("ConfirmRetry err = %v", err)
	}
}

func TestConfirmRetryPassesOnRetryRefusals(t *testing.T) {
	for _, refusal := range []error{jobs.ErrNotFound, jobs.ErrNothingToRetry, jobs.ErrNotRunning, jobs.ErrInvalidInput, jobs.ErrDryRunNotRetryable} {
		svc := agentenroll.NewService(enrolling(), errTargets{retryEr: refusal}, errAgents{}, errHosts{})
		list, err := svc.ConfirmRetry(ctx, uuid.New(), uuid.New(), nil, true)
		if err != nil || list != nil {
			t.Errorf("%v: list = %v, err = %v; want a pass with no list", refusal, list, err)
		}
	}
}

func TestConfirmRunUnknownTemplatePasses(t *testing.T) {
	svc := agentenroll.NewService(errTemplates{err: templates.ErrNotFound}, errTargets{}, errAgents{}, errHosts{})
	list, err := svc.ConfirmRun(ctx, uuid.New(), uuid.New(), nil, nil, false)
	if err != nil || list != nil {
		t.Fatalf("list = %v, err = %v", list, err)
	}
}
