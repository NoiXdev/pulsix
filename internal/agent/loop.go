// The agent loop: the agent binary's heartbeat. Forever: make sure a
// valid identity exists (enroll with the one-time token when not — never
// again afterwards), renew the certificate inside its last third of
// lifetime, then hold one AgentStream open — heartbeats at the configured
// cadence, facts at start and every facts interval, manager acks consumed
// — and reconnect with backoff (1 s doubling, capped at 60 s) on stream
// or dial errors. Context cancellation is the clean shutdown, the zero
// error: manager downtime is NEVER an error exit (a refused enrollment
// and a missing identity are — those are configuration, and systemd
// handles crashes by restarting; the loop only ever exits on shutdown or
// on something no retry can fix). A reconnect never re-enrolls: the
// identity on disk wins and is renewed, not replaced.
//
// Log hygiene: ids, durations and the agent's own name — never the token,
// the certificate or the key (pinned by the no-secrets log-capture test).
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// Sentinels the binary maps to exit codes. Both carry the same remediation
// (a fresh enrollment token is the fix); only the cause differs.
var (
	// ErrNoEnrollment: no valid identity on disk and no token to enroll
	// with — the operator must act.
	ErrNoEnrollment = errors.New("no valid agent identity in the data dir and no PULSIX_ENROLL_TOKEN to enroll with — create a new enrollment token on the manager, set PULSIX_ENROLL_TOKEN and restart the agent")
	// ErrEnrollRefused: the manager (or the token format) actively refused
	// the enrollment — expired, used up, unknown or a taken name.
	ErrEnrollRefused = errors.New("the manager refused the enrollment — create a new enrollment token and restart the agent with it")
)

// Loop wiring constants.
const (
	maxBackoff     = 60 * time.Second
	streamDrainWit = 2 * time.Second
)

// Collector gathers host facts for one push. The production
// implementation wraps internal/agent/facts.Collect; tests inject fakes.
type Collector interface {
	Collect(ctx context.Context) (map[string]string, error)
}

// EnrollClient is the pinned connection the first enrollment rides (the
// one connection kind that needs no client certificate).
type EnrollClient interface {
	Enroll(ctx context.Context, req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error)
	Close() error
}

// AgentClient is the mTLS connection the stream and renewals ride.
type AgentClient interface {
	Renew(ctx context.Context, req *agentpb.RenewRequest) (*agentpb.EnrollResponse, error)
	AgentStream(ctx context.Context) (Stream, error)
	Close() error
}

// Stream is the agent's bidi session: heartbeats and facts out,
// acknowledgements in. Context is the RPC's own — the loop cancels it to
// end the session from the send side, which unblocks a blocked Recv.
type Stream interface {
	Send(*agentpb.AgentMessage) error
	Recv() (*agentpb.ManagerMessage, error)
	CloseSend() error
	Context() context.Context
}

// Dialer opens the two connection kinds. The production dialer
// (dial.go) builds gRPC connections over TLS; the loop tests inject
// fakes.
type Dialer interface {
	// DialEnroll dials the manager pinned to the token's CA fingerprint —
	// the first-connect TLS the worker's enrollment mirrors.
	DialEnroll(ctx context.Context, tok enrolltoken.Token) (EnrollClient, error)
	// DialAgent dials mTLS presenting id's certificate.
	DialAgent(ctx context.Context, id *Identity) (AgentClient, error)
}

// Ticker is the periodic-tick source the stream session's send loop
// runs on; injected so cadence tests tick deterministically.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// tickerFactory builds one ticker per interval.
type tickerFactory func(time.Duration) Ticker

// bufferWindow is the disconnect buffer's size (checks.Buffer): 15
// minutes of samples at the configured intervals, per the design's own
// "Disconnected agent" decision.
const bufferWindow = 15 * time.Minute

// Loop is the agent's connection loop. NewLoop wires it; Run drives it
// until its context is cancelled.
type Loop struct {
	cfg    Config
	dial   Dialer
	facts  Collector
	log    *slog.Logger
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	ticker tickerFactory

	scheduler *checks.Scheduler
	buffer    *checks.Buffer

	// routeMu guards sender: which stream session, if any, currently
	// receives the scheduler's results. See handleResults, activateSender
	// and deactivateSender in session.go.
	routeMu sync.Mutex
	sender  *streamSender
}

// LoopOption overrides injected dependencies for tests.
type LoopOption func(*Loop)

// WithNow replaces the wall clock (validity windows, renewal checks).
func WithNow(fn func() time.Time) LoopOption { return func(l *Loop) { l.now = fn } }

// WithSleep replaces the interruptible backoff sleep (tests fake clocks
// this way; the production sleep waits on ctx).
func WithSleep(fn func(ctx context.Context, d time.Duration) error) LoopOption {
	return func(l *Loop) { l.sleep = fn }
}

// WithLogger replaces the structured stderr logger.
func WithLogger(logger *slog.Logger) LoopOption { return func(l *Loop) { l.log = logger } }

// WithTicker replaces the ticker factory (heartbeat and facts cadence).
func WithTicker(fn tickerFactory) LoopOption { return func(l *Loop) { l.ticker = fn } }

// NewLoop wires the loop. No connection is opened here — Run dials. The
// scheduler and buffer are built AFTER options are applied, so they use
// whatever clock and ticker factory the caller (or a test) installed via
// WithNow/WithTicker rather than the defaults.
func NewLoop(cfg Config, dial Dialer, facts Collector, opts ...LoopOption) *Loop {
	l := &Loop{
		cfg:    cfg,
		dial:   dial,
		facts:  facts,
		now:    time.Now,
		sleep:  interruptibleSleep,
		ticker: newWallTicker,
		log:    slog.Default(),
	}
	for _, o := range opts {
		o(l)
	}
	l.scheduler = checks.NewScheduler(checks.Registry(), l.now, l.checksTicker)
	l.buffer = checks.NewBuffer(bufferWindow, l.now)
	return l
}

// checksTicker adapts the loop's own ticker factory to
// internal/agent/checks's identically-shaped Ticker interface — a
// signature conversion only: any value satisfying agent.Ticker already
// satisfies checks.Ticker by having the same two methods.
func (l *Loop) checksTicker(d time.Duration) checks.Ticker { return l.ticker(d) }

// Run keeps the agent connected until ctx is cancelled. The state machine
// per iteration: a valid identity renews when the renewal window is open
// and then holds the stream; an invalid or missing identity enrolls (the
// token is the credential) or fails with the remediation error.
func (l *Loop) Run(ctx context.Context) error {
	id, err := LoadIdentity(l.cfg.DataDir)
	if err != nil {
		return fmt.Errorf("load agent identity from %s: %w", l.cfg.DataDir, err)
	}
	l.log.InfoContext(ctx, "pulsix agent starting", "manager", l.cfg.ManagerAddr,
		"data_dir", l.cfg.DataDir, "agent_name", l.cfg.AgentName, "version", l.cfg.Version,
		"heartbeat_interval", l.cfg.HeartbeatInterval, "facts_interval", l.cfg.FactsInterval,
		"config_interval", l.cfg.ConfigInterval)
	if !id.Valid(l.now()) {
		l.log.InfoContext(ctx, "no valid agent identity; enrollment path", "data_dir", l.cfg.DataDir)
	}

	// The scheduler runs for the whole process lifetime, independent of
	// any single stream session: a disconnected agent keeps measuring
	// into the buffer rather than stopping just because the connection
	// did ("Disconnected agent" decision). handleResults (session.go)
	// routes each batch to whichever session is currently connected, or
	// to the buffer when none is.
	//
	// Its own context is derived from ctx but cancelled unconditionally
	// when Run returns, on every path — not only "ctx was cancelled".
	// Run can also exit early on its own (ErrNoEnrollment,
	// ErrEnrollRefused, ...) while the CALLER's ctx stays alive; without
	// this, the scheduler goroutine would run on indefinitely with
	// nothing left to feed it. The two deferred closures below run in
	// LIFO order, so schedCancel fires BEFORE the bounded wait — the
	// wait almost never actually blocks.
	schedCtx, schedCancel := context.WithCancel(ctx)
	schedulerDone := make(chan error, 1)
	go func() { schedulerDone <- l.scheduler.Run(schedCtx, l.handleResults) }()
	defer func() {
		select {
		case <-schedulerDone:
		case <-time.After(streamDrainWit):
		}
	}()
	defer schedCancel()

	backoffShift := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if id.Valid(l.now()) {
			if id.NeedsRenewal(l.now()) {
				next, err := l.renew(ctx, id)
				if err != nil {
					if ctx.Err() != nil {
						return nil // shutdown, not a renewal failure
					}
					d := backoff(backoffShift)
					backoffShift = nextBackoffShift(backoffShift)
					l.log.WarnContext(ctx, "certificate renewal failed; backing off", "backoff", d, "err", err)
					if err := l.sleep(ctx, d); err != nil {
						return nil
					}
					continue
				}
				id = next
			}
			if err := l.streamSession(ctx, id); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				d := backoff(backoffShift)
				backoffShift = nextBackoffShift(backoffShift)
				l.logStreamError(ctx, err, d, id)
				if err := l.sleep(ctx, d); err != nil {
					return nil
				}
				continue
			}
			backoffShift = 0 // a clean session end resets the backoff
			continue
		}
		if l.cfg.EnrollToken == "" {
			// Nothing valid on disk and nothing to enroll with: no retry
			// fixes this, so the loop reports the remediation instead of
			// spinning.
			l.log.ErrorContext(ctx, ErrNoEnrollment.Error())
			return ErrNoEnrollment
		}
		next, err := l.enroll(ctx, id)
		switch {
		case err == nil:
			id = next
			backoffShift = 0
			l.log.InfoContext(ctx, "agent identity ready", "host_id", id.HostID())
		case errors.Is(err, ErrBadEnrollAnswer):
			// The manager answered but the material failed verification:
			// a broken or lying answer, not a transport problem — the
			// same remediation as a refusal.
			l.log.ErrorContext(ctx, err.Error())
			return fmt.Errorf("%w: %v", ErrEnrollRefused, err)
		case errors.Is(err, ErrSetupFailed):
			// A local setup failure (a read-only data dir, an unparseable
			// key): no retry fixes it, and no retry should keep burning
			// enrollment attempts against it.
			l.log.ErrorContext(ctx, err.Error())
			return err
		case isEnrollUnreachable(err):
			// Manager downtime or a network blip: transient, never an
			// error exit — systemd owns crash recovery, the loop owns
			// waiting out an unreachable manager.
			d := backoff(backoffShift)
			backoffShift = nextBackoffShift(backoffShift)
			l.log.WarnContext(ctx, "could not reach the manager to enroll; backing off",
				"backoff", d, "err", err)
			if err := l.sleep(ctx, d); err != nil {
				return nil
			}
		default:
			// A definite refusal (unknown/expired/used-up token, taken
			// name, malformed CSR): no retry fixes it.
			l.log.ErrorContext(ctx, ErrEnrollRefused.Error(), "err", err)
			return fmt.Errorf("%w: %v", ErrEnrollRefused, err)
		}
	}
}

// logStreamError reports a failed stream session. An Unauthenticated
// error is the manager refusing the presented certificate — revocation or
// a replacement, not a transport problem — and names the operator's fix;
// everything else is logged as the transient failure it is.
func (l *Loop) logStreamError(ctx context.Context, err error, d time.Duration, id *Identity) {
	if status.Code(err) == codes.Unauthenticated {
		l.log.ErrorContext(ctx, "the manager refused this agent's certificate (revoked, replaced or expired); "+
			"enroll anew with a new enrollment token if this agent should keep running",
			"backoff", d, "host_id", id.HostID())
		return
	}
	l.log.WarnContext(ctx, "agent stream failed; backing off", "backoff", d, "err", err)
}

// backoff is the exponential delay for the given shift, capped.
func backoff(shift int) time.Duration {
	d := time.Second << uint(shift)
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

// nextBackoffShift advances the shift; anything past the cap stays.
func nextBackoffShift(shift int) int {
	if shift < 6 {
		return shift + 1 // 1s << 6 = 64s is past the 60s cap
	}
	return shift
}

// drainRecv waits for the receive goroutine to notice the session is
// over, bounded — a cancel kills the RPC underneath it, so the wait is
// normally instant.
func (l *Loop) drainRecv(recvErr <-chan error) {
	select {
	case <-recvErr:
	case <-time.After(streamDrainWit):
	}
}

// interruptibleSleep is the production sleep: the full duration, or until
// ctx is done.
func interruptibleSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// probeWritable checks that dir accepts a create-and-remove of a
// temporary file, so a caller can refuse to spend an enrollment attempt
// on a write it already knows will fail (a read-only mount would burn the
// token and leave nothing on disk to show for it).
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".writable-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
