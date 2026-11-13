package webcheck

import (
	"context"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/dbcheck"
)

// Page names a single-page plugin (nginx, apache, caddy) for Collect: the
// plugin's name (the prefix of every failure text), its up metric and the
// metrics that get the failure when the round fails.
type Page struct {
	Name   string
	Up     string
	Others []string
}

// Collect is the shared round of a plugin that reads one status page: it
// checks the secret fields, fetches rawURL under dbcheck.CollectTimeout,
// parses the body and observes the result. Any failure is Down: up = 0 and
// one error observation per p.Others, the reason a fixed text.
//
// Caller-context contract (checks.Plugin): a context that has ended, before
// or during the round, returns nil observations and ctx.Err() and nothing
// else; the plugin's own deadline is a server that did not answer, so it is
// a Down "timed out". parse runs before the final context check and must
// not touch plugin state; observe runs after it (it moves rate baselines),
// so a cancelled round leaves the baselines alone and a failed parse never
// reaches observe.
func Collect[S any](ctx context.Context, p Page, rawURL string, params map[string]any, fields map[string]string,
	parse func(body []byte) (S, error), observe func(S) []checks.Observation) ([]checks.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	down := func(reason string) ([]checks.Observation, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return dbcheck.Down(p.Name, p.Up, p.Others, reason), nil
	}
	auth, err := ParseAuth(fields)
	if err != nil {
		return down(err.Error())
	}
	cctx, cancel := context.WithTimeout(ctx, dbcheck.CollectTimeout)
	defer cancel()
	body, reason := Fetch(cctx, rawURL, Options{VerifyTLS: VerifyTLS(params), Auth: auth})
	if reason != "" {
		return down(reason)
	}
	s, err := parse(body)
	if err != nil {
		return down(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return observe(s), nil
}
