package webcheck

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// Outcome is one page's result in a Round: the parsed page, or the fixed
// text of why it could not be fetched or parsed (Reason != "").
type Outcome[S any] struct {
	Page   S
	Reason string
}

// Fetcher fetches one page of a RoundEach under the round's context and
// options: the body, or nil and a fixed reason.
type Fetcher func(ctx context.Context, opt Options) (body []byte, reason string)

// Round is the shared round of the web server plugins: it checks the
// secret fields, then fetches every one of rawURLs concurrently (each
// under Fetch's own dbcheck.QueryTimeout, all under one
// dbcheck.CollectTimeout) and parses each body. The outcomes are in
// rawURLs' order. maxBody is Options.MaxBody (0 for the package's bound).
// Secret fields that are refused fail every page with the field's text and
// no request is sent.
//
// Caller-context contract (checks.Plugin): a context that has ended, before
// or during the round (the secret's failure included), returns nil
// outcomes and ctx.Err() and nothing else; the round's own deadline is a
// server that did not answer, so that page's reason is "timed out". parse
// runs concurrently, once per page, and must not touch plugin state: a
// caller observes the outcomes (and moves its rate baselines) only after
// Round returned without an error, so a cancelled round leaves the
// baselines alone. A panic while fetching or parsing a page is raised again
// on the caller's goroutine, after every page is done, as a fixed value
// (see fetchAll); it takes precedence over a cancelled caller context, as
// the re-panic happens before the context is checked.
func Round[S any](ctx context.Context, rawURLs []string, maxBody int, params map[string]any, fields map[string]string,
	parse func(body []byte) (S, error)) ([]Outcome[S], error) {
	fetch := make([]Fetcher, len(rawURLs))
	for i, rawURL := range rawURLs {
		fetch[i] = func(ctx context.Context, opt Options) ([]byte, string) { return Fetch(ctx, rawURL, opt) }
	}
	return RoundEach(ctx, fetch, maxBody, params, fields, parse)
}

// RoundEach is Round with a fetch function per page instead of a URL
// (phpfpm reads some pools over FastCGI, backlog 3.11a); everything else,
// the contract included, is Round's. A fetcher must honour its context and
// bound its own request like Fetch does.
func RoundEach[S any](ctx context.Context, fetch []Fetcher, maxBody int, params map[string]any, fields map[string]string,
	parse func(body []byte) (S, error)) ([]Outcome[S], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Outcome[S], len(fetch))
	if auth, err := ParseAuth(fields); err != nil {
		for i := range out {
			out[i].Reason = err.Error()
		}
	} else {
		fetchAll(ctx, fetch, Options{VerifyTLS: VerifyTLS(params), Auth: auth, MaxBody: maxBody}, parse, out)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// errPagePanicked is what Round panics with when fetching or parsing a page
// panicked. It is a fixed value: the original one could quote the page.
var errPagePanicked = errors.New("webcheck: page handler panicked")

// fetchAll fills out with every page's outcome, one goroutine per page,
// under one dbcheck.CollectTimeout. A goroutine's panic would end the
// agent (the scheduler recovers only the plugin's own goroutine), so each
// is recovered and, once every page is done, raised again on the caller's
// goroutine as errPagePanicked, where the scheduler's recovery of a
// SecretPlugin turns it into its fixed observation.
func fetchAll[S any](ctx context.Context, fetch []Fetcher, opt Options, parse func(body []byte) (S, error), out []Outcome[S]) {
	cctx, cancel := context.WithTimeout(ctx, dbcheck.CollectTimeout)
	defer cancel()
	panicked := make([]bool, len(fetch))
	var wg sync.WaitGroup
	for i, f := range fetch {
		wg.Go(func() {
			defer func() {
				if recover() != nil {
					panicked[i] = true
				}
			}()
			body, reason := f(cctx, opt)
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
	if slices.Contains(panicked, true) {
		panic(errPagePanicked)
	}
}
