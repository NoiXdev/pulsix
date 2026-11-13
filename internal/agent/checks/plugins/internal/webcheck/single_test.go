package webcheck_test

import (
	"context"
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

var page = webcheck.Page{Name: "x", Up: "x.up", Others: []string{"x.a", "x.b"}}

func parseOK(b []byte) (string, error) { return string(b), nil }

func TestCollectRunsParseThenObserve(t *testing.T) {
	srv := webchecktest.NewServer(t, "hello")
	obs, err := webcheck.Collect(t.Context(), page, srv.URL, nil, nil, parseOK,
		func(s string) []checks.Observation {
			return []checks.Observation{checks.LabelObservation("", "x.up", s)}
		})
	if err != nil || len(obs) != 1 || obs[0].Label == nil || *obs[0].Label != "hello" {
		t.Fatalf("Collect = %+v, %v", obs, err)
	}
}

func TestCollectFailuresAreDownAndSkipObserve(t *testing.T) {
	observe := func(string) []checks.Observation { t.Fatal("observe ran"); return nil }
	bad := webchecktest.NewServer(t, "")
	bad.Set(500, "")
	good := webchecktest.NewServer(t, "ok")
	tests := []struct {
		name   string
		url    string
		fields map[string]string
		parse  func([]byte) (string, error)
		want   string
	}{
		{"status", bad.URL, nil, parseOK, "HTTP status 500"},
		{"parse", good.URL, nil, func([]byte) (string, error) { return "", errors.New("unexpected response format") }, "unexpected response format"},
		{"secret", good.URL, map[string]string{}, parseOK, "secret field `basic_auth_user` is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, err := webcheck.Collect(t.Context(), page, tt.url, nil, tt.fields, tt.parse, observe)
			if err != nil {
				t.Fatal(err)
			}
			webchecktest.MustDown(t, obs, "x", "x.up", page.Others, tt.want)
		})
	}
	if len(good.URIs()) != 1 {
		t.Fatalf("the bad-secret round sent a request: %v", good.URIs())
	}
}

func TestCollectCallerContextEnding(t *testing.T) {
	srv := webchecktest.NewServer(t, "ok")
	observe := func(string) []checks.Observation { t.Fatal("observe ran"); return nil }
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	obs, err := webcheck.Collect(ctx, page, srv.URL, nil, nil, parseOK, observe)
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect = %+v, %v; want nil, context.Canceled", obs, err)
	}
	if len(srv.URIs()) != 0 {
		t.Fatal("a request was sent with a dead context")
	}
	// Cancelled during parse: still only the error, observe never runs.
	ctx, cancel = context.WithCancel(t.Context())
	obs, err = webcheck.Collect(ctx, page, srv.URL, nil, nil,
		func(b []byte) (string, error) { cancel(); return string(b), nil }, observe)
	if !errors.Is(err, context.Canceled) || obs != nil {
		t.Fatalf("Collect = %+v, %v; want nil, context.Canceled", obs, err)
	}
}
