package webcheck

import (
	"context"
	"sync"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// Outcome is one page's result in a Round: the parsed page, or the fixed
// text of why it could not be fetched or parsed (Reason != "").
type Outcome[S any] struct {
	Page   S
	Reason string
}

// Round is the shared round of the web server plugins: it checks the
// secret fields, then fetches every one of rawURLs concurrently (each
// under Fetch's own dbcheck.QueryTimeout, all under one
// dbcheck.CollectTimeout) and parses each body. The outcomes are in
// rawURLs' order. maxBody is Options.MaxBody (0 for the package's bound).
// Secret fields that are refused fail every page with the field's text and
// no request is sent.
//
// Caller-context contract (checks.Plugin): a context that has ended, before
// or during the round, returns nil outcomes and ctx.Err() and nothing else;
// the round's own deadline is a server that did not answer, so that page's
// reason is "timed out". parse runs concurrently, once per page, and must
// not touch plugin state: a caller observes the outcomes (and moves its
// rate baselines) only after Round returned without an error, so a
// cancelled round leaves the baselines alone.
func Round[S any](ctx context.Context, rawURLs []string, maxBody int, params map[string]any, fields map[string]string,
	parse func(body []byte) (S, error)) ([]Outcome[S], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Outcome[S], len(rawURLs))
	auth, err := ParseAuth(fields)
	if err != nil {
		for i := range out {
			out[i].Reason = err.Error()
		}
		return out, nil
	}
	cctx, cancel := context.WithTimeout(ctx, dbcheck.CollectTimeout)
	defer cancel()
	opt := Options{VerifyTLS: VerifyTLS(params), Auth: auth, MaxBody: maxBody}
	var wg sync.WaitGroup
	for i, rawURL := range rawURLs {
		wg.Go(func() {
			body, reason := Fetch(cctx, rawURL, opt)
			if reason != "" {
				out[i].Reason = reason
				return
			}
			page, err := parse(body)
			if err != nil {
				out[i].Reason = err.Error()
				return
			}
			out[i].Page = page
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
