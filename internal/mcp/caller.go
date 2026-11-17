package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// caller dispatches a tool's REST call in-process, as the token that called
// the endpoint, with the endpoint's context mark.
type caller struct {
	router        http.Handler
	authorization string
	slug          string
	areas         []tenancy.Area
	publicURL     string
}

// response is one dispatched REST response.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// orgPath builds /api/v1/orgs/{slug}<suffix>. Every path segment a tool
// fills from its arguments goes through id first, so an argument can never
// steer the request to a route the tool does not name.
func (c *caller) orgPath(format string, ids ...string) (string, error) {
	segs := make([]any, len(ids))
	for i, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("%q is not a valid id", raw)
		}
		segs[i] = id.String()
	}
	return "/api/v1/orgs/" + url.PathEscape(c.slug) + fmt.Sprintf(format, segs...), nil
}

// do serves one request through the router and returns a tool error for
// anything that is not a 2xx response.
func (c *caller) do(ctx context.Context, method, path string, query url.Values, body any, ifMatch string) (response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	// The outer request's chi route context must not leak into the inner
	// one: chi reuses a route context it finds and would route the inner
	// path against the outer match.
	inner := context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	req := httptest.NewRequestWithContext(auth.WithAssistantEndpoint(inner), method, target, reader)
	req.Header.Set("Authorization", c.authorization)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	res := response{Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
	if res.Status >= 300 {
		return res, problemError(res)
	}
	return res, nil
}

// get decodes a GET's JSON body into out.
func (c *caller) get(ctx context.Context, path string, query url.Values, out any) (response, error) {
	res, err := c.do(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return res, err
	}
	if out != nil {
		if err := json.Unmarshal(res.Body, out); err != nil {
			return res, fmt.Errorf("decode response: %w", err)
		}
	}
	return res, nil
}

// errChanged is the refusal of a write whose revision is stale.
var errChanged = errors.New("changed meanwhile; read it again")

// problemError turns a REST problem into the tool error the assistant
// reads: title and detail (plus field errors), never the request.
func problemError(res response) error {
	if res.Status == http.StatusPreconditionFailed {
		return errChanged
	}
	if res.Status >= 500 {
		return errors.New("the request failed inside Pulsix; try again later")
	}
	var p struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Errors []struct {
			Message  string `json:"message"`
			Location string `json:"location"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(res.Body, &p); err != nil || (p.Title == "" && p.Detail == "") {
		return errors.New(http.StatusText(res.Status))
	}
	msg := p.Title
	if p.Detail != "" {
		msg += ": " + p.Detail
	}
	var fields []string
	for _, e := range p.Errors {
		fields = append(fields, strings.TrimPrefix(e.Location, "body.")+" "+e.Message)
	}
	if len(fields) > 0 {
		msg += " (" + strings.Join(fields, "; ") + ")"
	}
	return errors.New(msg)
}
