package webcheck_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

// TestRoundEachRunsEveryFetcher: each page is fetched by its own function,
// under the round's options (the secret's login, the body bound), and the
// outcomes are in order.
func TestRoundEachRunsEveryFetcher(t *testing.T) {
	var (
		mu   sync.Mutex
		opts []webcheck.Options
	)
	fetch := func(body, reason string) webcheck.Fetcher {
		return func(_ context.Context, opt webcheck.Options) ([]byte, string) {
			mu.Lock()
			defer mu.Unlock()
			opts = append(opts, opt)
			return []byte(body), reason
		}
	}
	got, err := webcheck.RoundEach(t.Context(), []webcheck.Fetcher{fetch("ok", "")}, 7, map[string]any{"verify_tls": false},
		map[string]string{"basic_auth_user": webchecktest.PlantUser}, parseOK)
	if err != nil || len(got) != 1 || got[0].Page != "ok" {
		t.Fatalf("RoundEach = %+v, %v", got, err)
	}
	if o := opts[0]; o.MaxBody != 7 || o.VerifyTLS || !o.Auth.Set || o.Auth.User != webchecktest.PlantUser {
		t.Fatalf("options = %+v", o)
	}
	got, err = webcheck.RoundEach(t.Context(), []webcheck.Fetcher{fetch("", "timed out"), fetch("x", "")}, 0, nil, nil, parseOK)
	if err != nil || got[0].Reason != "timed out" || got[1].Page != "x" {
		t.Fatalf("RoundEach = %+v, %v", got, err)
	}
}

// TestRoundEachDeadline: a fetcher's context carries the round's deadline.
func TestRoundEachDeadline(t *testing.T) {
	var left time.Duration
	f := func(ctx context.Context, _ webcheck.Options) ([]byte, string) {
		dl, _ := ctx.Deadline()
		left = time.Until(dl)
		return []byte("ok"), ""
	}
	if _, err := webcheck.RoundEach(t.Context(), []webcheck.Fetcher{f}, 0, nil, nil, parseOK); err != nil {
		t.Fatal(err)
	}
	if left <= 0 || left > 10*time.Second {
		t.Fatalf("deadline in %v, want within the round's 10 s", left)
	}
}

func TestStatusReason(t *testing.T) {
	for code, want := range map[int]string{
		200: "", 401: "authentication failed (HTTP 401)", 403: "permission denied (HTTP 403)",
		404: "HTTP status 404", 204: "HTTP status 204", 302: "HTTP status 302",
	} {
		if got := webcheck.StatusReason(code); got != want {
			t.Errorf("StatusReason(%d) = %q, want %q", code, got, want)
		}
	}
}

// TestFetchRefusesFastCGIEntriesWithoutDialing pins what an agent that
// predates backlog 3.11a does with a phpfpm FastCGI entry: its HTTP fetch
// refuses the scheme before dialing, so the pool reads down with
// "connection failed" rather than anything else being read.
func TestFetchRefusesFastCGIEntriesWithoutDialing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			_ = c.Close()
		}
	}()
	for _, e := range []string{"fcgi://" + ln.Addr().String() + "/status?json", "unix:/run/php/fpm.sock:/status?json"} {
		if body, reason := webcheck.Fetch(t.Context(), e, webcheck.Options{}); body != nil || reason != "connection failed" {
			t.Fatalf("Fetch(%q) = %q, %q", e, body, reason)
		}
	}
	select {
	case <-accepted:
		t.Fatal("Fetch dialed a FastCGI entry")
	case <-time.After(100 * time.Millisecond):
	}
}
