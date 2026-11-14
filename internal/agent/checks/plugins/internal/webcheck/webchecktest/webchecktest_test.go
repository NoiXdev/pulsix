package webchecktest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

func TestServerRecordsAndAnswers(t *testing.T) {
	srv := webchecktest.NewServer(t, "x")
	srv.Set(404, "y")
	if len(srv.Auths()) != 0 || len(srv.URIs()) != 0 {
		t.Fatal("requests recorded before any call")
	}
}

func TestMustDownSet(t *testing.T) {
	v := 0.0
	obs := []checks.Observation{
		{Metric: "x.up", Value: &v},
		checks.ErrObservation("", "x.a", errors.New("x: boom")),
	}
	webchecktest.MustDown(t, obs, "x", "x.up", []string{"x.a"}, "boom")
	webchecktest.MustErr(t, obs, "x", "x.a", "boom")
	webchecktest.MustAbsent(t, obs, "x.b")
	webchecktest.NoSecretIn(t, webchecktest.NewServer(t, ""), obs, nil)
}

func TestServerAnswersWithWhatWasSetAndRecordsTheRequest(t *testing.T) {
	srv := webchecktest.NewServer(t, "first")
	srv.Set(http.StatusTeapot, "second")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/status?auto", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(webchecktest.PlantUser, webchecktest.PlantPass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTeapot || string(body) != "second" {
		t.Errorf("answer = %d %q, want %d %q", resp.StatusCode, body, http.StatusTeapot, "second")
	}
	if uris := srv.URIs(); len(uris) != 1 || uris[0] != "/status?auto" {
		t.Errorf("URIs = %q, want [/status?auto]", uris)
	}
	if auths := srv.Auths(); len(auths) != 1 || !strings.HasPrefix(auths[0], "Basic ") {
		t.Errorf("Auths = %q, want one Basic header", auths)
	}
}

func TestHangingServerNeverAnswers(t *testing.T) {
	srv := webchecktest.NewHangingServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("got an answer %d, want none", resp.StatusCode)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
}

func TestClockAdvances(t *testing.T) {
	c := webchecktest.NewClock()
	start := c.Now()
	c.Advance(90 * time.Second)
	if got := c.Now().Sub(start); got != 90*time.Second {
		t.Errorf("advanced by %v, want 90s", got)
	}
}

func TestMustValueAndNoSecretInAnError(t *testing.T) {
	v := 3.0
	obs := []checks.Observation{{Metric: "x.n", Value: &v}}
	webchecktest.MustValue(t, obs, "x.n", 3)
	if _, ok := webchecktest.Find(obs, "x.missing"); ok {
		t.Error("Find reported a metric that is not there")
	}
	webchecktest.NoSecretIn(t, webchecktest.NewServer(t, ""), obs, errors.New("connection refused"))
}
