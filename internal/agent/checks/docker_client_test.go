package checks

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeEngine serves h on a Unix socket under a short temp dir (macOS caps
// socket paths at 104 bytes) and returns the socket path.
func fakeEngine(t *testing.T, h http.Handler) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fake; the named-pipe variant covers Windows")
	}
	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return path
}

func engineFor(t *testing.T, h http.Handler) dockerEngine {
	t.Helper()
	e, err := newDockerEngine(fakeEngine(t, h))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestClientListsAllContainers(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("all") != "1" {
			t.Errorf("all = %q", q.Get("all"))
		}
		if _, ok := q["filters"]; ok {
			t.Error("filters must be absent without a label filter")
		}
		_, _ = w.Write([]byte(`[
		 {"Id":"aaaaaaaaaaaaaaaaaaaa","Names":["/web"],"State":"Running","Labels":{"a":"b"}},
		 {"Id":"bbbbbbbbbbbbbbbbbbbb","Names":["/db","/web/db"],"State":"exited","Labels":null}]`))
	}))
	got, err := e.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d containers", len(got))
	}
	if got[0].Name != "web" || got[0].State != "running" || got[0].Labels["a"] != "b" || got[0].ID != "aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Name != "db" || got[1].State != "exited" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestClientNamesFallBackToShortID(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"Id":"0123456789abcdef0123","Names":[],"State":"created"},{"Id":"abc","State":"created"}]`))
	}))
	got, err := e.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "0123456789ab" || got[1].Name != "abc" {
		t.Errorf("names = %q, %q", got[0].Name, got[1].Name)
	}
}

func TestClientSendsTheLabelFilter(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var f map[string][]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &f); err != nil {
			t.Errorf("filters: %v", err)
		}
		if len(f) != 1 || len(f["label"]) != 1 || f["label"][0] != "pulsix.monitor=true" {
			t.Errorf("filters = %v", f)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	if _, err := e.List(context.Background(), "pulsix.monitor=true"); err != nil {
		t.Fatal(err)
	}
}

func TestClientInspects(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		switch r.URL.Path {
		case "/containers/c1/json":
			_, _ = w.Write([]byte(`{"RestartCount":4,"State":{"ExitCode":137,"Health":{"Status":"unhealthy"}},"Config":{"Env":["SECRET=x"]}}`))
		case "/containers/c2/json":
			_, _ = w.Write([]byte(`{"RestartCount":0,"State":{"ExitCode":0}}`))
		case "/containers/c3/json":
			_, _ = w.Write([]byte(`{"State":{"Health":{"Status":""}}}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	got, err := e.Inspect(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (containerDetail{Health: "unhealthy", RestartCount: 4, ExitCode: 137}); got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
	for _, id := range []string{"c2", "c3"} {
		got, err = e.Inspect(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Health != "none" {
			t.Errorf("%s: health = %q", id, got.Health)
		}
	}
}

func TestClientInspectGone(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such container", http.StatusNotFound)
	}))
	if _, err := e.Inspect(context.Background(), "x"); !errors.Is(err, errContainerGone) {
		t.Errorf("err = %v", err)
	}
}

func TestClientNeverQuotesTheBody(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("TOPSECRET"))
	}))
	_, err := e.List(context.Background(), "")
	_, err2 := e.Inspect(context.Background(), "x")
	for _, err := range []error{err, err2} {
		if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "TOPSECRET") {
			t.Errorf("err = %v", err)
		}
	}
}

// TestClientExplainsARefusedRequest: an engine that does not serve the
// request at all (an API it no longer supports, for one) answers 400. The
// error says so in plain words and, like every other status, never quotes
// the body.
func TestClientExplainsARefusedRequest(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("client version 1.41 is too old TOPSECRET"))
	}))
	_, err := e.List(context.Background(), "")
	if err == nil || err.Error() != "docker: list containers: engine refused the request (status 400)" {
		t.Errorf("list err = %v", err)
	}
	_, err = e.Inspect(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "engine refused the request (status 400)") || strings.Contains(err.Error(), "TOPSECRET") {
		t.Errorf("inspect err = %v", err)
	}
}

// objectBody answers 200 with a JSON object of n bytes (braces around spaces),
// which decodes cleanly into an inspect, so only the cap can refuse it.
func objectBody(n int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{`))
		_, _ = w.Write([]byte(strings.Repeat(" ", n-2)))
		_, _ = w.Write([]byte(`}`))
	})
}

func TestClientCapsTheListBody(t *testing.T) {
	// A JSON array that would decode cleanly without the cap.
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[`))
		_, _ = w.Write([]byte(strings.Repeat(" ", 17<<20)))
		_, _ = w.Write([]byte(`]`))
	}))
	_, err := e.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Errorf("err = %v", err)
	}
}

func TestClientCapsTheInspectBody(t *testing.T) {
	e := engineFor(t, objectBody(5<<20))
	_, err := e.Inspect(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Errorf("err = %v", err)
	}
}

func TestClientAcceptsAnInspectBodyJustUnderTheCap(t *testing.T) {
	e := engineFor(t, objectBody(4<<20))
	got, err := e.Inspect(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Health != "none" {
		t.Errorf("got %+v", got)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	e := engineFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			t.Error("redirect was followed")
		}
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	_, err := e.List(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Errorf("err = %v", err)
	}
}

func TestClientStopsAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	e := engineFor(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	// Registered after the server's Close so it runs first and frees the handler.
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := e.List(ctx, ""); err == nil {
		t.Error("expected an error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestClientNoSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path")
	}
	path := filepath.Join(os.TempDir(), "pulsix-no-such-dir", "d.sock")
	e, err := newDockerEngine(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.List(context.Background(), ""); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v", err)
	}
}
