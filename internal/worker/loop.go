// The claim loop: the worker binary's heartbeat. It dials nothing itself —
// the mTLS client connection is injected. Forever: claim a job of its
// zones, run it through the Executor port while streaming every event back
// over a RunEvents stream bound to the claim's token, and report the
// terminal result. Empty queues pause ClaimInterval; claim failures back
// off exponentially (1 s doubling, capped at 30 s, reset by any successful
// claim); context cancellation exits cleanly.
//
// Concurrency in v0.2b is one job at a time: the loop claims again only
// after the previous run's result is reported — "claims pause while a job
// runs", per the plan.
//
// Seq numbering: the loop numbers events per run, starting at 1, which
// orders one stream. A job the manager requeues (at-least-once after a dead
// worker) is claimed into a NEW runOne with a NEW stream whose seq starts
// fresh at 1 — the manager therefore assigns the STORED seq itself
// (strictly increasing per job across attempts) and stamps each event with
// its claim attempt (internal/jobs AppendEvent).
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// Status is a terminal job state; the set is the manager's closed one.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusError     Status = "error"
)

// valid reports whether s is one of the four terminal states ReportResult
// accepts.
func (s Status) valid() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusError:
		return true
	}
	return false
}

// RunEnv is the environment one run executes in. WorkDir is a fresh private
// temp dir under the data dir, removed when the run ends; RepoCacheDir is
// the persistent per-data-dir cache git repos sync into.
type RunEnv struct {
	WorkDir      string
	RepoCacheDir string
	Config       Config
}

// Executor runs one claimed job, streaming events through the sink. Task
// 9's ansible-runner and Task 12's sshprobe implement it. The loop owns
// job_id, seq and the event timestamp; the executor fills host, task,
// state and payload.
type Executor interface {
	Run(ctx context.Context, job *workerpb.Job, env RunEnv, sink func(*workerpb.RunEvent) error) (Status, map[string]any, error)
}

// Loop wiring constants.
const (
	maxBackoff       = 30 * time.Second
	reportTimeout    = 10 * time.Second
	streamDrainWait  = 10 * time.Second
	repoCacheDirName = "repo-cache"
)

// The RunEvents stream's binding metadata. These EXACT names are what the
// manager's bindStream reads (internal/grpcsrv/events.go); a mismatch here
// fails every stream open with PermissionDenied, so the end-to-end loop
// test pins them behaviorally against the real harness.
const (
	metadataJobID       = "job_id"
	metadataWorkerToken = "worker_token"
)

// Loop is the worker's claim loop. NewLoop wires it; Run drives it until
// its context is cancelled — cancellation is the clean shutdown, the zero
// error.
type Loop struct {
	cfg    Config
	client workerpb.WorkerServiceClient
	exec   Executor

	sleep    func(ctx context.Context, d time.Duration) error
	now      func() time.Time
	log      *slog.Logger
	identity *Identity
}

// LoopOption overrides injected dependencies for tests.
type LoopOption func(*Loop)

// WithSleep replaces the interruptible sleep (tests fake clocks this way).
func WithSleep(fn func(ctx context.Context, d time.Duration) error) LoopOption {
	return func(l *Loop) { l.sleep = fn }
}

// WithNow replaces the wall clock (event timestamps, run durations).
func WithNow(fn func() time.Time) LoopOption {
	return func(l *Loop) { l.now = fn }
}

// WithLogger replaces the structured stderr logger.
func WithLogger(logger *slog.Logger) LoopOption {
	return func(l *Loop) { l.log = logger }
}

// WithIdentity gives the loop the worker's own Identity, so an
// Unauthenticated claim error can tell a genuinely revoked/expired
// certificate (the on-disk one is bad; delete and re-enroll) apart from a
// stale connection presenting a certificate that has since been renewed
// (a healthy identity; retrying or reconnecting heals it — see
// ReconnectingClient). Without it, every Unauthenticated is reported as
// the latter, worse case.
func WithIdentity(id *Identity) LoopOption {
	return func(l *Loop) { l.identity = id }
}

// NewLoop wires the loop over an already-dialed client (worker.Dial builds
// one for the binary; the bufconn harness does for tests).
func NewLoop(cfg Config, client workerpb.WorkerServiceClient, exec Executor, opts ...LoopOption) *Loop {
	l := &Loop{
		cfg:    cfg,
		client: client,
		exec:   exec,
		sleep:  interruptibleSleep,
		now:    time.Now,
		log:    slog.Default(),
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Run claims and executes until ctx is done, then returns nil. Claim
// failures (and failed result reports) back off exponentially; everything
// is logged with ids, kinds and durations only — never payload material
// (pinned by the no-secrets log-capture test).
func (l *Loop) Run(ctx context.Context) error {
	if l.cfg.MaxConcurrent > 1 {
		l.log.WarnContext(ctx, "PULSIX_MAX_CONCURRENT above 1 is not supported in v0.2b; running one job at a time",
			"configured", l.cfg.MaxConcurrent)
	}
	if err := os.MkdirAll(filepath.Join(l.cfg.DataDir, repoCacheDirName), 0o700); err != nil {
		return fmt.Errorf("create repo cache dir: %w", err)
	}

	zoneIDs := make([]string, len(l.cfg.Zones))
	for i, z := range l.cfg.Zones {
		zoneIDs[i] = z.String()
	}

	backoffShift := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		resp, err := l.client.ClaimJob(ctx, &workerpb.ClaimJobRequest{
			Version: l.cfg.Version, ZoneIds: zoneIDs,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutdown, not a claim failure
			}
			d := backoff(backoffShift)
			backoffShift = nextBackoffShift(backoffShift)
			if status.Code(err) == codes.Unauthenticated {
				l.logUnauthenticated(ctx, d)
			} else {
				l.log.WarnContext(ctx, "claim failed; backing off", "backoff", d, "err", err)
			}
			if err := l.sleep(ctx, d); err != nil {
				return nil
			}
			continue
		}
		backoffShift = 0 // a successful claim resets the backoff
		job := resp.GetJob()
		if job == nil {
			if err := l.sleep(ctx, l.cfg.ClaimInterval); err != nil {
				return nil
			}
			continue
		}

		start := l.now()
		l.log.InfoContext(ctx, "job claimed", "job_id", job.GetId(), "kind", job.GetKind())
		status, reportErr := l.runOne(ctx, job)
		attrs := []any{"job_id", job.GetId(), "kind", job.GetKind(), "status", string(status), "duration", l.now().Sub(start)}
		if reportErr != nil {
			l.log.ErrorContext(ctx, "job finished; result report failed", append(attrs, "err", reportErr)...)
			d := backoff(backoffShift)
			backoffShift = nextBackoffShift(backoffShift)
			if err := l.sleep(ctx, d); err != nil {
				return nil
			}
		} else {
			l.log.InfoContext(ctx, "job finished", attrs...)
		}
	}
}

// logUnauthenticated reports a claim refused with Unauthenticated. When
// the loop knows its own Identity (WithIdentity) and that certificate is
// still within its validity window, this is not a bad identity — it is
// almost always a connection that handshook before a certificate renewal
// and has not yet moved to a fresh one (see ReconnectingClient), which
// heals itself; the message says so instead of sending the operator to
// delete a healthy worker. Only once the certificate itself has expired
// (or WithIdentity was never set — the embedded worker's short-lived
// process, or a caller predating this fix) does it point at revocation.
func (l *Loop) logUnauthenticated(ctx context.Context, backoffDelay time.Duration) {
	if l.identity != nil && l.now().Before(l.identity.Leaf().NotAfter) {
		l.log.ErrorContext(ctx, "the manager refused this worker's certificate even though it is still valid and not expired; "+
			"this is usually a connection that has not yet reconnected with a renewed certificate and recovers on its own — "+
			"if it does not, the worker may have been revoked",
			"backoff", backoffDelay, "not_after", l.identity.Leaf().NotAfter)
		return
	}
	// Revoked, unknown, or (with no Identity to tell otherwise) possibly
	// just expired: retrying cannot help. A revoked worker's row survives
	// on the manager (workers.Delete is revoked-only), so it must be
	// deleted on the Workers page before the same name can enroll again;
	// a bundle-flow worker needs a fresh `worker add` bundle instead of a
	// token.
	l.log.ErrorContext(ctx, "the manager refused this worker's certificate (revoked, unknown, or expired); "+
		"if it was revoked, delete it on the Workers page, then re-enroll — a new token, or a fresh `worker add` bundle",
		"backoff", backoffDelay)
}

// backoff is the exponential delay for the given shift, capped.
func backoff(shift int) time.Duration {
	if shift > 30 {
		return maxBackoff
	}
	d := time.Second << uint(shift)
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

// nextBackoffShift advances the shift; anything beyond 5 (1 s << 5 = 32 s)
// is past the cap already.
func nextBackoffShift(shift int) int {
	if shift < 5 {
		return shift + 1
	}
	return shift
}

// runOne executes one claimed job: bind the stream, run, forward, report.
// It always reports a terminal status — even a failed stream open becomes
// an error-status report, because a claimed job whose worker goes silent
// can only be recovered by the manager's reaper.
func (l *Loop) runOne(ctx context.Context, job *workerpb.Job) (Status, error) {
	jobID := job.GetId()
	token := job.GetWorkerToken()
	if token == "" {
		return l.report(ctx, job, StatusError, nil, "claim carries no worker token")
	}

	streamCtx := metadata.AppendToOutgoingContext(ctx, metadataJobID, jobID, metadataWorkerToken, token)
	stream, err := l.client.RunEvents(streamCtx)
	if err != nil {
		return l.report(ctx, job, StatusError, nil, fmt.Sprintf("open run event stream: %v", err))
	}

	workDir, err := l.newWorkDir()
	if err != nil {
		return l.report(ctx, job, StatusError, nil, fmt.Sprintf("create work dir: %v", err))
	}
	defer func() { _ = os.RemoveAll(workDir) }() // best-effort: the dir is scratch

	runCtx, cancelRun := context.WithCancel(streamCtx)
	defer cancelRun()

	// The manager sends at most one cancel directive; applying it cancels
	// the executor's derived context. Recv runs on its own goroutine —
	// Send (in the sink) and Recv may run concurrently. recvDone closes
	// when the manager has ended the stream.
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		for {
			d, err := stream.Recv()
			if err != nil {
				return // EOF or transport death: nothing more to apply
			}
			if d.GetCancel() {
				cancelRun()
			}
		}
	}()

	env := RunEnv{
		WorkDir:      workDir,
		RepoCacheDir: filepath.Join(l.cfg.DataDir, repoCacheDirName),
		Config:       l.cfg,
	}
	seq := 0
	status, stats, execErr := l.exec.Run(runCtx, job, env, func(ev *workerpb.RunEvent) error {
		// The loop owns identity and time; clone so the executor's event
		// is never mutated behind its back.
		out := proto.Clone(ev).(*workerpb.RunEvent)
		out.Payload = BoundEventPayload(out.GetPayload())
		out.JobId = jobID
		out.Seq = uint64(seq + 1)
		out.At = timestamppb.New(l.now())
		if err := stream.Send(out); err != nil {
			return fmt.Errorf("send run event: %w", err)
		}
		seq++
		return nil
	})

	final := status
	errText := ""
	switch {
	case execErr != nil && runCtx.Err() != nil && errors.Is(execErr, context.Canceled):
		final = StatusCancelled // the directive (or shutdown) cancelled the run
	case execErr != nil:
		final = StatusError
		errText = execErr.Error()
	case !status.valid():
		final = StatusError
		errText = fmt.Sprintf("executor returned unknown status %q", string(status))
	}

	if err := stream.CloseSend(); err != nil {
		l.log.WarnContext(ctx, "close run event stream", "job_id", jobID, "err", err)
	}
	// ReportResult and RunEvents are independent RPCs, and the manager's
	// finish reads the stored events (a connection test's result event, a
	// sync or rotation outcome). The manager ends the stream only after it
	// has stored every event the worker sent, so the report waits for that
	// — bounded, so a stuck stream still gets its result reported.
	select {
	case <-recvDone:
	case <-time.After(streamDrainWait):
		l.log.WarnContext(ctx, "run event stream did not end before the result report", "job_id", jobID, "waited", streamDrainWait)
	}
	return l.report(ctx, job, final, stats, errText)
}

// report is the terminal ReportResult write. It runs on a detached, bounded
// context so the result lands even when the loop's context is already
// cancelled by shutdown. Stats the encoder cannot represent degrade the
// result deterministically to the error status — the manager's run view is
// built on the stats, so a run recorded "successful" without them would
// lie; the error text names the marshalling failure.
func (l *Loop) report(ctx context.Context, job *workerpb.Job, status Status, stats map[string]any, errText string) (Status, error) {
	var statsJSON []byte
	if stats != nil {
		var err error
		if statsJSON, err = json.Marshal(stats); err != nil {
			l.log.ErrorContext(ctx, "marshal job stats", "job_id", job.GetId(), "err", err)
			status = StatusError
			errText = joinErrText(errText, fmt.Sprintf("report stats: %v", err))
		}
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
	defer cancel()
	if _, err := l.client.ReportResult(reportCtx, &workerpb.ReportResultRequest{
		JobId:  job.GetId(),
		Status: string(status),
		Stats:  string(statsJSON),
		Error:  errText,
	}); err != nil {
		return status, fmt.Errorf("report result: %w", err)
	}
	return status, nil
}

// joinErrText joins two halves of a result's error text, keeping the
// executor's own reason first when there is one.
func joinErrText(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// newWorkDir creates a fresh owner-private temp dir under the data dir; the
// caller removes it when the run ends.
func (l *Loop) newWorkDir() (string, error) {
	return os.MkdirTemp(l.cfg.DataDir, "job-")
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

// truncationHeadBytes is how much of an oversized payload the truncation
// marker keeps: enough to read what the event was about, well inside
// MaxEventPayloadBytes even after JSON escaping (at most 6 bytes per byte).
const truncationHeadBytes = 64 << 10

// BoundEventPayload keeps one event's payload within
// workerpb.MaxEventPayloadBytes. A payload under the bound passes untouched;
// a larger one is replaced by a JSON marker that keeps the event in the
// run's record: {"truncated": true, "original_bytes": n, "head": "..."}.
// Executors redact before they emit (the runner does), so the head never
// carries a secret the full payload would not have lost to redaction.
func BoundEventPayload(payload string) string {
	if len(payload) <= workerpb.MaxEventPayloadBytes {
		return payload
	}
	head := payload[:truncationHeadBytes]
	// Never cut inside a UTF-8 sequence: back off to a rune boundary.
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	b, err := json.Marshal(struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
		Head          string `json:"head"`
	}{true, len(payload), head})
	if err != nil {
		return `{"truncated":true}`
	}
	return string(b)
}
