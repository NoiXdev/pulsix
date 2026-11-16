package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// bearerTransport adds the Authorization header the endpoint authenticates.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

// connect opens an MCP client session against the test server's endpoint.
func connect(t *testing.T, s *apitest.Server, token string) (*sdk.ClientSession, error) {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "pulsix-test", Version: "1"}, nil)
	return client.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint:   s.HTTP.URL + "/api/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}},
		MaxRetries: -1,
	}, nil)
}

func mustConnect(t *testing.T, s *apitest.Server, token string) *sdk.ClientSession {
	t.Helper()
	cs, err := connect(t, s, token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *sdk.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	out := map[string]bool{}
	for _, tl := range res.Tools {
		out[tl.Name] = true
	}
	return out
}

// call invokes a tool and returns its text and whether it is a tool error.
func call(t *testing.T, cs *sdk.ClientSession, name string, args any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// A protocol-level refusal (an unknown or hidden tool) counts as an
		// error the assistant sees.
		return err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// callOK calls a tool that must succeed and decodes its JSON text into out.
func callOK(t *testing.T, cs *sdk.ClientSession, name string, args any, out any) string {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s returned a tool error: %s", name, text)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			t.Fatalf("%s: decode %q: %v", name, text, err)
		}
	}
	return text
}

func toolParams(name string, args any) *sdk.CallToolParams {
	return &sdk.CallToolParams{Name: name, Arguments: args}
}
