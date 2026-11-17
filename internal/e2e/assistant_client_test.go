// The assistant suite's clients: an MCP client session per assistant token
// (the Go MCP SDK over Streamable HTTP, the shape internal/mcp's own tests
// use), plain bearer requests for the REST refusals, and the owner's
// session requests that need headers (If-Match) the apiClient does not send.
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// bearerTransport adds the Authorization header the endpoint authenticates.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// connect opens an MCP client session at /api/mcp with token as bearer.
func (a *assistantSuite) connect(token string) (*sdk.ClientSession, error) {
	client := sdk.NewClient(&sdk.Implementation{Name: "pulsix-e2e", Version: "1"}, nil)
	return client.Connect(a.ctx, &sdk.StreamableClientTransport{
		Endpoint:   a.s.api.base + "/api/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}, Timeout: 60 * time.Second},
		MaxRetries: -1,
	}, nil)
}

// mustConnect is connect for a token that must get in.
func (a *assistantSuite) mustConnect(token string) *sdk.ClientSession {
	a.s.t.Helper()
	cs, err := a.connect(token)
	if err != nil {
		a.s.t.Fatalf("connect to /api/mcp: %v", err)
	}
	a.s.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// listTools returns the names tools/list shows on the session, in order.
func (a *assistantSuite) listTools(cs *sdk.ClientSession) []string {
	a.s.t.Helper()
	var out []string
	for tl, err := range cs.Tools(a.ctx, nil) {
		if err != nil {
			a.s.t.Fatalf("tools/list: %v", err)
		}
		out = append(out, tl.Name)
	}
	return out
}

// toolProperties maps each listed tool to the argument names its input
// schema declares; the schemas refuse any other property.
func (a *assistantSuite) toolProperties(cs *sdk.ClientSession) map[string]map[string]bool {
	a.s.t.Helper()
	out := map[string]map[string]bool{}
	for tl, err := range cs.Tools(a.ctx, nil) {
		if err != nil {
			a.s.t.Fatalf("tools/list: %v", err)
		}
		raw, err := json.Marshal(tl.InputSchema)
		if err != nil {
			a.s.t.Fatal(err)
		}
		var sch struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &sch); err != nil {
			a.s.t.Fatal(err)
		}
		m := map[string]bool{}
		for k := range sch.Properties {
			m[k] = true
		}
		out[tl.Name] = m
	}
	return out
}

// call invokes a tool and returns its text and whether it is an error the
// assistant sees (a tool error, or a protocol refusal of an unknown tool).
// It does not record the call.
func (a *assistantSuite) call(cs *sdk.ClientSession, name string, args any) (string, bool) {
	a.s.t.Helper()
	res, err := cs.CallTool(a.ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
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

// record is call for the every_tool and run_request phases: the tool is
// marked called and its output kept for the secret sweep.
func (a *assistantSuite) record(cs *sdk.ClientSession, name string, args any) (string, bool) {
	a.s.t.Helper()
	text, isErr := a.call(cs, name, args)
	a.called[name] = true
	a.outputs = append(a.outputs, toolOutput{tool: name, text: text})
	return text, isErr
}

// recordOK is record for a call that must succeed; its JSON text is
// decoded into out when out is not nil.
func (a *assistantSuite) recordOK(cs *sdk.ClientSession, name string, args, out any) string {
	a.s.t.Helper()
	text, isErr := a.record(cs, name, args)
	if isErr {
		a.s.t.Fatalf("%s returned an error: %s", name, text)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			a.s.t.Fatalf("%s: decode %q: %v", name, text, err)
		}
	}
	return text
}

// rawRequest sends one JSON request with client and returns the status,
// the response headers and the body, asserting nothing.
func rawRequest(client *http.Client, method, url string, header http.Header, body any) (int, http.Header, []byte, error) {
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw, err
}

// bearer sends one request with token as bearer (no session cookie) and
// returns its status and body.
func (s *scenario) bearer(method, path, token string, body any) (int, string) {
	s.t.Helper()
	status, _, raw, err := rawRequest(http.DefaultClient, method, s.api.base+path,
		http.Header{"Authorization": {"Bearer " + token}}, body)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	return status, string(raw)
}

// ownerSend is the owner's session request with extra headers; it asserts
// a 2xx status, decodes the body into out and returns the response headers.
func (s *scenario) ownerSend(method, path string, header http.Header, body, out any) http.Header {
	s.t.Helper()
	status, h, raw, err := rawRequest(s.api.http, method, s.api.base+path, header, body)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	if status < 200 || status >= 300 {
		s.t.Fatalf("%s %s: status %d, body %s", method, path, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
	return h
}

// tokenCreated is the token create response's fields the suite reads.
type tokenCreated struct {
	Token string `json:"token"`
	ID    string `json:"id"`
}

// createToken creates an org token through the owner's session.
func (s *scenario) createToken(body map[string]any) tokenCreated {
	s.t.Helper()
	var out tokenCreated
	s.post("/api/v1/"+s.apiPath()+"/tokens", body, &out)
	if out.Token == "" || out.ID == "" {
		s.t.Fatalf("token create returned %+v", out)
	}
	return out
}

// createAssistantToken creates an owner-role assistant token with areas.
func (s *scenario) createAssistantToken(name string, areas ...tenancy.Area) tokenCreated {
	s.t.Helper()
	return s.createToken(map[string]any{"name": name, "role": "owner", "kind": "assistant", "areas": areaStrings(areas)})
}

func areaStrings(areas []tenancy.Area) []string {
	out := make([]string, len(areas))
	for i, a := range areas {
		out[i] = string(a)
	}
	return out
}

// setAssistantAccess turns the organization's assistant switch on or off,
// keeping the follow-up setting.
func (s *scenario) setAssistantAccess(on bool) {
	s.t.Helper()
	s.put("/api/v1/"+s.apiPath()+"/settings/assistant", map[string]any{"assistant_enabled": on}, nil)
}

// sortedCopy returns a sorted copy of names.
func sortedCopy(names []string) []string {
	out := slices.Clone(names)
	slices.Sort(out)
	return out
}
