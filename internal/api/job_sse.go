// The run view's SSE route (backlog 2.12): a subscriber of one job's
// event stream, fed by the jobs broker the gRPC append path fills. A
// stream always begins with `event: status` carrying the CURRENT status
// (so a late subscriber renders immediately), tails `event: log` frames
// as the worker appends events, and reports every status change the same
// way — the terminal status frame is the stream's last, after which it
// ends. A client that disconnects unsubscribes (the request context's
// cancellation is what the loop selects on), so a closed tab leaves
// nothing behind.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// jobStatusPollInterval is how often a live stream re-reads the job's
// status between broker events. The broker carries the log lines the
// worker appends; the status transitions (claim, cancel confirmation,
// the terminal finish) are seen through this poll, which covers every
// path a status can change — including the ones no event rides on.
const jobStatusPollInterval = 500 * time.Millisecond

// jobStreamAccessInterval is how often a live stream re-checks that its
// caller may still read the run: the credential it was opened with still
// valid, the membership still there, the role still allowing jobs.read.
// The stream authorised once and then ran until the job finished, so a
// removed member or a signed-out session kept reading the output (backlog
// 7.22). Access ends within this interval, not at the next log line.
const jobStreamAccessInterval = 2 * time.Second

// registerJobStreamRoute wires GET /orgs/{org}/jobs/{id}/stream. It is a
// read (jobs.read) and, being a read, not audited — the same rule the
// activity feed's own read routes follow.
func registerJobStreamRoute(hapi huma.API, deps Deps) {
	huma.Get(hapi, "/api/v1/orgs/{org}/jobs/{id}/stream", func(ctx context.Context, in *jobStreamInput) (*huma.StreamResponse, error) {
		scope, err := requireOrg(ctx, deps, in.Org, tenancy.PermJobsRead)
		if err != nil {
			return nil, err
		}
		id, err := parseUUID(in.ID, "job id")
		if err != nil {
			return nil, err
		}
		// An unknown job is a normal 404 error response, decided here
		// before any stream frame exists — the SSE framing cannot carry
		// a status code once it started.
		if _, err := deps.Jobs.Get(ctx, scope.OrgID, id); err != nil {
			return nil, mapJobsErr(ctx, err)
		}
		// The request's own context: net/http cancels it on client
		// disconnect, and the loop below selects on that, so a closed
		// browser tab unsubscribes itself. The org scope the middleware
		// bound rides on it too, so every store statement inside stays
		// inside the organization.
		return &huma.StreamResponse{
			Body: func(hctx huma.Context) {
				writeJobStream(hctx, ctx, deps, scope.OrgID, id)
			},
		}, nil
	})
}

// writeJobStream is the stream loop Body runs: subscribe first (so an
// event appended while the initial status is being read is buffered, not
// lost), then the current status, then the tail until the terminal
// status frame.
func writeJobStream(hctx huma.Context, rctx context.Context, deps Deps, orgID, jobID uuid.UUID) {
	w := hctx.BodyWriter()
	// Headers before the status: huma's SetStatus writes the response
	// header immediately, so the SSE content type must be set first or
	// Go's default text/plain locks in.
	hctx.SetHeader("Content-Type", "text/event-stream; charset=utf-8")
	hctx.SetHeader("Cache-Control", "no-cache")
	hctx.SetHeader("X-Accel-Buffering", "no") // do not let a proxy buffer the stream
	hctx.SetStatus(http.StatusOK)
	flush, _ := w.(http.Flusher)

	// writeFrame writes one SSE frame and flushes it to the client; a
	// write failure (a broken pipe) ends the stream.
	writeFrame := func(event string, data any) error {
		if err := writeSSEFrame(w, event, data); err != nil {
			return err
		}
		if flush != nil {
			flush.Flush()
		}
		return nil
	}

	events, cancel, err := deps.Jobs.Subscribe(rctx, orgID, jobID, 0)
	if err != nil {
		// The job vanished between requireOrg and here (or a wiring
		// failure): the SSE framing cannot carry a clean 404 anymore, so
		// say so in one error frame and end — the SPA falls back to
		// polling, which answers 404 properly.
		streamError(rctx, writeFrame, "subscribe", err)
		return
	}
	defer cancel() // a returning stream unsubscribes; so does a cancelled request, below

	// The initial status frame: the CURRENT status at subscription time,
	// whatever it is — the late subscriber renders immediately.
	j, err := deps.Jobs.Get(rctx, orgID, jobID)
	if err != nil {
		streamError(rctx, writeFrame, "read initial job status", err)
		return
	}
	lastStatus := j.Status
	if err := writeFrame("status", map[string]any{"id": j.ID.String(), "status": string(j.Status)}); err != nil {
		return
	}
	if j.Status.Terminal() {
		return // subscribing to a finished run: one status frame, end
	}

	ticker := time.NewTicker(jobStatusPollInterval)
	defer ticker.Stop()
	access := time.NewTicker(jobStreamAccessInterval)
	defer access.Stop()
	for {
		select {
		case <-access.C:
			if err := streamAccessHeld(rctx, deps, orgID); err != nil {
				switch {
				case errors.Is(err, context.Canceled):
				case errors.Is(err, errStreamAccessEnded):
					slog.InfoContext(rctx, "job event stream ended", "job_id", jobID, "reason", err)
					_ = writeFrame("error", map[string]any{"error": "access to this run ended"})
				default:
					// Fail closed: a check that cannot be made ends the
					// stream; the SPA falls back to polling.
					streamError(rctx, writeFrame, "re-check access", err)
				}
				return
			}
		case <-rctx.Done():
			// Client disconnect: the deferred cancel above unsubscribes —
			// no subscriber is left behind.
			return
		case ev, ok := <-events:
			if !ok {
				return // the subscription was cancelled under us
			}
			if err := writeFrame("log", toJobEventDTO(ev)); err != nil {
				return
			}
		case <-ticker.C:
			j, err := deps.Jobs.Get(rctx, orgID, jobID)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				// The job vanished mid-stream (it does not happen — jobs
				// are not deleted — but a stream must not spin on an
				// error either): end the stream after one error frame.
				streamError(rctx, writeFrame, "poll job status", err)
				return
			}
			if j.Status != lastStatus {
				lastStatus = j.Status
				err := writeFrame("status", map[string]any{"id": j.ID.String(), "status": string(j.Status)})
				if j.Status.Terminal() || err != nil {
					return // the terminal status frame ends the stream
				}
			}
		}
	}
}

// errStreamAccessEnded is why streamAccessHeld refuses: the credential,
// the membership or the role no longer allows reading the run.
var errStreamAccessEnded = errors.New("access ended")

// streamAccessHeld re-checks, for a running stream, what requireOrg
// checked when it opened: the credential it was opened with is still
// valid (a session not signed out, expired or of a disabled user; a token
// not revoked or expired), still MFA-complete, and the caller is still a
// member whose role — narrowed by the token's own, as requireOrg does —
// allows jobs.read.
func streamAccessHeld(ctx context.Context, deps Deps, orgID uuid.UUID) error {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return errStreamAccessEnded
	}
	if p.TokenID != nil {
		usable, err := deps.Tokens.StillUsable(ctx, *p.TokenID)
		if err != nil {
			return fmt.Errorf("check token: %w", err)
		}
		if !usable {
			return fmt.Errorf("%w: token no longer usable", errStreamAccessEnded)
		}
	} else {
		mfaComplete, err := deps.Sessions.StillValid(ctx, p.SessionID)
		if errors.Is(err, auth.ErrUnauthenticated) || (err == nil && !mfaComplete) {
			return fmt.Errorf("%w: session no longer valid", errStreamAccessEnded)
		}
		if err != nil {
			return fmt.Errorf("check session: %w", err)
		}
	}
	role, err := deps.Tenancy.MembershipOf(ctx, orgID, p.UserID)
	if errors.Is(err, tenancy.ErrNotMember) || errors.Is(err, tenancy.ErrNotFound) {
		return fmt.Errorf("%w: no longer a member", errStreamAccessEnded)
	}
	if err != nil {
		return fmt.Errorf("check membership: %w", err)
	}
	if ts, ok := auth.TokenScopeFromContext(ctx); ok {
		role = tenancy.Lower(role, tenancy.Role(ts.Role))
	}
	if !tenancy.Allows(role, tenancy.PermJobsRead) {
		return fmt.Errorf("%w: role no longer allows reading runs", errStreamAccessEnded)
	}
	return nil
}

// writeSSEFrame writes one frame in the text/event-stream wire format:
//
//	event: <kind>
//	data: <one line of JSON>
//	(blank line)
//
// and reports a write failure — a broken client pipe — so the stream
// loop can stop instead of writing into a dead connection.
func writeSSEFrame(w io.Writer, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode %s frame: %w", event, err) // skipped, not half-written
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return fmt.Errorf("write %s frame: %w", event, err)
	}
	return nil
}

// streamError is a running stream's failure: the detail goes to the server
// log, the client gets one generic error frame. Nothing internal — database
// messages, addresses, other rows' ids — ever reaches the browser; the SPA
// answers any error frame by falling back to polling, which reports proper
// HTTP statuses.
func streamError(ctx context.Context, writeFrame func(string, any) error, stage string, err error) {
	slog.ErrorContext(ctx, "job event stream failed", "stage", stage, "err", err)
	_ = writeFrame("error", map[string]any{"error": "the event stream failed; reload to continue"})
}
