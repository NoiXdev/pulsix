package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

// recordingPusher captures what pushAffected asked for AND the state of
// the context it was asked on. The context is the point of these tests:
// an apitest-level assertion cannot see it, because inside a huma handler
// the request context has not been cancelled yet whether or not
// pushAffected detaches it — so the only place the detachment is
// observable is here, calling pushAffected directly with a context that
// is already done.
type recordingPusher struct {
	hosts    [][]uuid.UUID
	orgs     []uuid.UUID
	ctxErr   error
	deadline time.Time
	err      error
}

func (p *recordingPusher) PushHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) error {
	p.capture(ctx)
	p.hosts = append(p.hosts, hostIDs)
	return p.err
}

func (p *recordingPusher) PushOrg(ctx context.Context, orgID uuid.UUID) error {
	p.capture(ctx)
	p.orgs = append(p.orgs, orgID)
	return p.err
}

func (p *recordingPusher) capture(ctx context.Context) {
	p.ctxErr = ctx.Err()
	p.deadline, _ = ctx.Deadline()
}

// TestPushAffectedSurvivesACancelledRequest is the detachment itself: the
// write has already committed when pushAffected runs, so a client that
// gave up — or a proxy that cut the response — must not take the push
// down with it and leave some agents told and others not. Removing
// context.WithoutCancel from pushAffected fails this test.
func TestPushAffectedSurvivesACancelledRequest(t *testing.T) {
	orgID := uuid.Must(uuid.NewV7())
	hostID := uuid.Must(uuid.NewV7())

	for _, tc := range []struct {
		name     string
		affected checks.Affected
		check    func(t *testing.T, p *recordingPusher)
	}{
		{
			name:     "a named set of hosts",
			affected: checks.Affected{Hosts: []uuid.UUID{hostID}},
			check: func(t *testing.T, p *recordingPusher) {
				if len(p.hosts) != 1 || len(p.hosts[0]) != 1 || p.hosts[0][0] != hostID {
					t.Fatalf("pushed %+v, want one batch naming the affected host", p.hosts)
				}
				if len(p.orgs) != 0 {
					t.Fatalf("a host set must not take the organization-wide path: %+v", p.orgs)
				}
			},
		},
		{
			name:     "the whole organization",
			affected: checks.Affected{WholeOrg: true},
			check: func(t *testing.T, p *recordingPusher) {
				if len(p.orgs) != 1 || p.orgs[0] != orgID {
					t.Fatalf("pushed %+v, want one organization-wide push", p.orgs)
				}
				if len(p.hosts) != 0 {
					t.Fatalf("an organization-wide set must not fan out: %+v", p.hosts)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &recordingPusher{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // the client is already gone
			if ctx.Err() == nil {
				t.Fatal("the fixture's own context is not cancelled")
			}

			pushAffected(ctx, Deps{AgentPush: p}, orgID, tc.affected)

			if p.ctxErr != nil {
				t.Fatalf("the push ran on a cancelled context (%v); it must outlive the request it followed", p.ctxErr)
			}
			if p.deadline.IsZero() {
				t.Fatal("the push ran without a deadline; detached work must still be bounded")
			}
			if remaining := time.Until(p.deadline); remaining <= 0 || remaining > pushTimeout {
				t.Fatalf("deadline in %s, want a live bound of at most %s", remaining, pushTimeout)
			}
			tc.check(t, p)
		})
	}
}

// TestPushAffectedNeverPushesNothing: an empty affected set must not reach
// the pusher at all — a write that turns out to affect no host (a
// monitoring group nothing binds to) must cost nothing downstream either.
func TestPushAffectedNeverPushesNothing(t *testing.T) {
	p := &recordingPusher{}
	pushAffected(context.Background(), Deps{AgentPush: p}, uuid.Must(uuid.NewV7()), checks.Affected{})
	if len(p.hosts) != 0 || len(p.orgs) != 0 {
		t.Fatalf("an empty affected set pushed %+v / %+v, want nothing", p.hosts, p.orgs)
	}
}

// TestPushAffectedSwallowsAFailure: the write it follows has already
// committed, and the agents re-assert their configuration version every 60
// seconds, so a push that cannot be delivered is logged — never turned
// into an error the handler would report.
func TestPushAffectedSwallowsAFailure(t *testing.T) {
	p := &recordingPusher{err: errors.New("no stream")}
	// Nothing to assert but that it returns: pushAffected has no error
	// result to propagate, which is the contract.
	pushAffected(context.Background(), Deps{AgentPush: p}, uuid.Must(uuid.NewV7()),
		checks.Affected{Hosts: []uuid.UUID{uuid.Must(uuid.NewV7())}})
	if len(p.hosts) != 1 {
		t.Fatalf("the failing push was not attempted: %+v", p.hosts)
	}
}
