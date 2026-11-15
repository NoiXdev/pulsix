package http_test

import (
	"context"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/http"
)

// run is the http probe called the way the registry calls it.
func run(ctx context.Context, g *probe.Guard, params map[string]any, address string) []probe.Observation {
	return http.NewProbe(nil)(ctx, g, params, address, nil)
}

// runWith is run with the secret's fields.
func runWith(ctx context.Context, g *probe.Guard, params map[string]any, address string, secret map[string]string) []probe.Observation {
	return http.NewProbe(nil)(ctx, g, params, address, secret)
}
