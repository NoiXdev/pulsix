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

// upHostMetrics is how many whole-host metrics an attempt over http to an
// IP literal reports when it is up: up, failure, status_code,
// response_ms, connect_ms, ttfb_ms, transfer_ms, redirects and
// content_bytes.
const upHostMetrics = 9

// failAllHostMetrics are the whole-host metrics an attempt at an http URL
// of an IP literal or {address} that could not be made puts its error on:
// those every up attempt of that URL reports.
var failAllHostMetrics = []string{
	"http.up", "http.failure", "http.status_code", "http.response_ms",
	"http.connect_ms", "http.ttfb_ms", "http.transfer_ms", "http.redirects", "http.content_bytes",
}

// failAllNamedMetrics is failAllHostMetrics for an http URL of a name,
// which every up attempt looks up.
var failAllNamedMetrics = []string{
	"http.up", "http.failure", "http.status_code", "http.response_ms",
	"http.dns_ms", "http.connect_ms", "http.ttfb_ms", "http.transfer_ms", "http.redirects", "http.content_bytes",
}
