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
// is Round over rawURL alone, then observe on the parsed page. Any failure
// is Down: up = 0 and one error observation per p.Others, the reason a
// fixed text. Round's caller-context contract carries over: a context that
// ends, before or during the round, returns nil observations and ctx.Err()
// and nothing else, and observe (which moves rate baselines) never runs
// then, nor after a failed fetch or parse.
func Collect[S any](ctx context.Context, p Page, rawURL string, params map[string]any, fields map[string]string,
	parse func(body []byte) (S, error), observe func(S) []checks.Observation) ([]checks.Observation, error) {
	out, err := Round(ctx, []string{rawURL}, 0, params, fields, parse)
	if err != nil {
		return nil, err
	}
	if out[0].Reason != "" {
		return dbcheck.Down(p.Name, p.Up, p.Others, out[0].Reason), nil
	}
	return observe(out[0].Page), nil
}
