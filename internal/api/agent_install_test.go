package api_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// The agent distribution through the real router: the two routes a host
// being installed actually calls, anonymously, on a fully wired manager —
// no session, no organization, no CSRF. The handlers' own behaviour is
// pinned in agent_install_internal_test.go; what this adds is that they are
// mounted, reachable without a credential, and templated with the public
// URL the manager was built with.
func TestAgentDistributionIsPublicOnTheRealRouter(t *testing.T) {
	binary := []byte("pretend-this-is-an-agent")
	s := apitest.New(t, apitest.WithAgentBinaries(fstest.MapFS{
		"pulsix-agent-linux-amd64":        {Data: binary},
		"pulsix-agent-linux-amd64.sha256": {Data: []byte("abc123  pulsix-agent-linux-amd64\n")},
	}))

	t.Run("install.sh", func(t *testing.T) {
		resp, err := http.Get(s.HTTP.URL + "/install.sh")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if want := `BASE_URL="` + s.HTTP.URL + `"`; !strings.Contains(string(body), want) {
			t.Errorf("the served script must be templated with the manager's own URL (%s)", want)
		}
	})

	t.Run("install.ps1", func(t *testing.T) {
		resp, err := http.Get(s.HTTP.URL + "/install.ps1")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if want := "$BaseUrl = '" + s.HTTP.URL + "'"; !strings.Contains(string(body), want) {
			t.Errorf("the served script must be templated with the manager's own URL (%s)", want)
		}
	})

	t.Run("binary", func(t *testing.T) {
		resp, err := http.Get(s.HTTP.URL + "/agents/bin/linux/amd64")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != string(binary) {
			t.Errorf("body = %q, want the embedded binary", body)
		}
	})
}

// A manager built without the agent binaries — what every test build and
// every `go build ./cmd/manager` without `make agent-binaries` produces —
// still serves the install script; only the download says what is missing.
func TestAgentBinaryRouteWithoutADistributionOnTheRealRouter(t *testing.T) {
	s := apitest.New(t)

	resp, err := http.Get(s.HTTP.URL + "/agents/bin/linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "make agent-binaries") {
		t.Errorf("the refusal must name the build step: %s", body)
	}
}

// The assistant connect script is public like install.sh and is served
// byte for byte, untemplated, even on a manager with no public URL.
func TestAssistantConnectScriptIsPublicOnTheRealRouter(t *testing.T) {
	s := apitest.New(t)
	resp, err := http.Get(s.HTTP.URL + "/assistant-mcp.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/x-shellscript; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	body, _ := io.ReadAll(resp.Body)
	want, err := os.ReadFile("../../scripts/assistant-mcp.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Error("the served body must equal scripts/assistant-mcp.sh")
	}
}
