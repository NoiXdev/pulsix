package webcheck_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

func TestRoundFetchesAndParsesEveryPageInOrder(t *testing.T) {
	a := webchecktest.NewServer(t, "a")
	b := webchecktest.NewServer(t, "b")
	b.Set(http.StatusForbidden, "")
	c := webchecktest.NewServer(t, "c")
	parse := func(body []byte) (string, error) {
		if string(body) == "c" {
			return "", errors.New("unexpected response format")
		}
		return strings.ToUpper(string(body)), nil
	}
	got, err := webcheck.Round(t.Context(), []string{a.URL, b.URL, c.URL}, 0, nil, nil, parse)
	if err != nil {
		t.Fatal(err)
	}
	want := []webcheck.Outcome[string]{{Page: "A"}, {Reason: "permission denied (HTTP 403)"}, {Reason: "unexpected response format"}}
	if len(got) != len(want) {
		t.Fatalf("Round = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outcome %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The pages are fetched concurrently: two pages that each answer after
// 1 s are both in flight at once.
func TestRoundFetchesConcurrently(t *testing.T) {
	var inFlight, most atomic.Int32
	slow := func() *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Second)
			_, _ = w.Write([]byte("ok"))
		}))
		t.Cleanup(s.Close)
		return s
	}
	a, b := slow(), slow()
	got, err := webcheck.Round(t.Context(), []string{a.URL, b.URL}, 0, nil, nil, parseOK)
	if err != nil || got[0].Page != "ok" || got[1].Page != "ok" {
		t.Fatalf("Round = %+v, %v", got, err)
	}
	if most.Load() != 2 {
		t.Fatalf("not concurrent: %d in flight at most", most.Load())
	}
}

func TestRoundBadSecretSendsNothing(t *testing.T) {
	srv := webchecktest.NewServer(t, "ok")
	got, err := webcheck.Round(t.Context(), []string{srv.URL, srv.URL + "/2"}, 0, nil,
		map[string]string{"password": webchecktest.PlantPass}, parseOK)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range got {
		if o.Reason != "secret field `basic_auth_user` is required" {
			t.Fatalf("Round = %+v", got)
		}
	}
	if len(got) != 2 || len(srv.URIs()) != 0 {
		t.Fatalf("Round = %+v, requests %v", got, srv.URIs())
	}
}

func TestRoundPassesTheBodyLimit(t *testing.T) {
	srv := webchecktest.NewServer(t, strings.Repeat("x", 11))
	got, err := webcheck.Round(t.Context(), []string{srv.URL}, 10, nil, nil, parseOK)
	if err != nil || got[0].Reason != "response too large" {
		t.Fatalf("Round = %+v, %v", got, err)
	}
}

func TestRoundCallerContextEnding(t *testing.T) {
	srv := webchecktest.NewServer(t, "ok")
	hung := webchecktest.NewHangingServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := webcheck.Round(ctx, []string{srv.URL}, 0, nil, nil, parseOK)
	if !errors.Is(err, context.Canceled) || got != nil || len(srv.URIs()) != 0 {
		t.Fatalf("Round with a dead context = %+v, %v, requests %v", got, err, srv.URIs())
	}
	// Ended while one page hangs and the other answered: nothing but the
	// error, however far the other page got.
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	got, err = webcheck.Round(ctx, []string{srv.URL, hung.URL}, 0, nil, nil, parseOK)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("Round cancelled mid-round = %+v, %v", got, err)
	}
	// A bad secret with a dead context is still only the error.
	got, err = webcheck.Round(ctx, []string{srv.URL}, 0, nil, map[string]string{}, parseOK)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("Round with a bad secret and a dead context = %+v, %v", got, err)
	}
}

func TestRoundOwnDeadlineIsATimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 5 s request deadline")
	}
	hung := webchecktest.NewHangingServer(t)
	srv := webchecktest.NewServer(t, "ok")
	start := time.Now()
	got, err := webcheck.Round(t.Context(), []string{hung.URL, srv.URL}, 0, nil, nil, parseOK)
	if err != nil || got[0].Reason != "timed out" || got[1].Page != "ok" {
		t.Fatalf("Round = %+v, %v", got, err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A panic while fetching or parsing a page happens on a goroutine of
// Round's own; it is surfaced on the caller's goroutine, after every page
// is done, as a fixed value that carries nothing of the page, so the
// scheduler's recovery of a SecretPlugin (checks.collectSecret) catches it.
func TestRoundPanicSurfacesOnTheCallersGoroutine(t *testing.T) {
	boom := webchecktest.NewServer(t, "boom "+webchecktest.PlantPass)
	slow := webchecktest.NewServer(t, "slow")
	var slowDone atomic.Bool
	parse := func(body []byte) (string, error) {
		if strings.HasPrefix(string(body), "boom") {
			panic("page said " + string(body))
		}
		time.Sleep(200 * time.Millisecond)
		slowDone.Store(true)
		return string(body), nil
	}
	var got any
	func() {
		defer func() { got = recover() }()
		_, _ = webcheck.Round(t.Context(), []string{boom.URL, slow.URL}, 0, nil, nil, parse)
	}()
	if got == nil || fmt.Sprint(got) != "webcheck: page handler panicked" {
		t.Fatalf("recovered %v, want the fixed panic value", got)
	}
	if !slowDone.Load() {
		t.Fatal("Round panicked before every page was done")
	}
}
