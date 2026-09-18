package api

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/triggers"
)

// hookInput carries the raw body and every header; Resolve captures the
// headers and the TCP peer, which the signature check and the allowlist
// need (huma has no typed field for a header whose name the trigger picks).
type hookInput struct {
	ID      string `path:"id"`
	RawBody []byte
	header  http.Header
	remote  string
}

func (in *hookInput) Resolve(ctx huma.Context) []error {
	in.header = http.Header{}
	ctx.EachHeader(func(name, value string) { in.header.Add(name, value) })
	in.remote = ctx.RemoteAddr()
	return nil
}

type hookOutput struct {
	Body struct {
		RunID  string `json:"run_id,omitempty" doc:"The started run's first job"`
		Status string `json:"status,omitempty" enum:"skipped" doc:"skipped when on_active is skip and a run is active"`
	}
}

// registerHookRoutes mounts the two public webhook endpoints on the root
// API: no org in the path, no session or bearer requirement — the trigger's
// secret is the credential (spec §3). They are audited by the triggers
// service itself (actor webhook:<id>), hence allowlisted in the audit
// coverage test.
func registerHookRoutes(hapi huma.API, deps Deps) {
	for _, h := range []struct {
		path, op, summary string
		kind              triggers.Kind
	}{
		{"/api/v1/hooks/jobs/{id}", "receiveJobHook", "Start a job template run from a webhook", triggers.KindTemplateRun},
		{"/api/v1/hooks/repos/{id}", "receiveRepoHook", "Sync a git repository from a webhook", triggers.KindRepoSync},
	} {
		huma.Register(hapi, huma.Operation{
			OperationID: h.op, Method: http.MethodPost, Path: h.path, Summary: h.summary, Tags: []string{"Webhooks"},
			// +1: spec §3 accepts a body up to and including 1 MiB, but
			// huma's MaxBytesReader treats a body that reads exactly
			// MaxBodyBytes as too large regardless of whether more data
			// follows it, so the boundary itself needs one extra byte of
			// headroom to be accepted (Task 10 review, Minor 1).
			DefaultStatus: http.StatusAccepted, MaxBodyBytes: triggers.MaxBodyBytes + 1,
		}, func(ctx context.Context, in *hookInput) (*hookOutput, error) {
			res, err := deps.Triggers.Receive(ctx, triggers.Request{
				TriggerID: in.ID, Kind: h.kind, Body: in.RawBody, Header: in.header.Get,
				SourceIP: deps.ClientIP.Resolve(in.remote, in.header.Values("X-Forwarded-For")),
			})
			if err != nil {
				slog.ErrorContext(ctx, "webhook receive failed", "err", err)
				return nil, huma.Error500InternalServerError("internal error")
			}
			return hookResponse(res)
		})
		// huma marks a RawBody request required by default, which answers
		// every bodyless call with 400 before the handler ever runs. Spec
		// §3 lists no 400 for the hook endpoints — a trigger that maps
		// nothing (a plain "ping", or a bearer/token-authenticated call)
		// must be able to fire with no body at all (Task 10 review,
		// Important 1). This is the same *Operation huma.Register just
		// added to the document, so mutating it here reaches the handler
		// closure above.
		hapi.OpenAPI().Paths[h.path].Post.RequestBody.Required = false
	}
}

// hookResponse maps an outcome to HTTP. Every refusal that could reveal
// whether a trigger exists is the same empty 404.
func hookResponse(res triggers.Result) (*hookOutput, error) {
	out := &hookOutput{}
	switch res.Outcome {
	case triggers.OutcomeTriggered:
		out.Body.RunID = res.RunID.String()
		return out, nil
	case triggers.OutcomeSkippedActive:
		out.Body.Status = "skipped"
		return out, nil
	case triggers.OutcomeRateLimited:
		secs := int(math.Ceil(res.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		return nil, huma.ErrorWithHeaders(huma.Error429TooManyRequests("rate limited"), http.Header{"Retry-After": {strconv.Itoa(secs)}})
	case triggers.OutcomeInvalidPayload:
		return nil, huma.Error422UnprocessableEntity("the payload could not be mapped",
			&huma.ErrorDetail{Location: res.Field, Message: "missing or invalid value"})
	case triggers.OutcomeFailed:
		return nil, huma.Error409Conflict("the trigger's target cannot run right now")
	}
	return nil, huma.Error404NotFound("")
}
