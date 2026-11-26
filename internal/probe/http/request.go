package http

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/noixdev/pulsix/internal/probe/httpreq"
)

var (
	errBadHeader = errors.New("invalid request header")
	errBadBody   = errors.New("invalid request body")
	// errWritesOff is the error of a state-changing request while the
	// manager's operator has not turned them on; it names the setting.
	errWritesOff = errors.New("state-changing requests are turned off on this server (PULSIX_PROBE_WRITE_METHODS)")
)

// request is what a check asks to send besides its URL (backlog 3.84):
// the method, the headers in order and the body with its content type
// (none when hasBody is false). It never prints its values.
type request struct {
	method      string
	headers     [][2]string
	body        string
	contentType string
	hasBody     bool
}

func (request) String() string   { return "http request" }
func (request) GoString() string { return "http request" }

// requestOf reads the method, headers, body and content_type params by
// internal/probe/httpreq's rules, the ones the descriptor validated with.
// Params that never passed that validation are refused here too, with an
// error that quotes none of them: an unknown method, a headers value that
// is not a list of acceptable entries, a body with a method that takes
// none, over the bound or not a string, a content type that is not a
// media type or comes without a body.
func requestOf(params map[string]any) (request, error) {
	r := request{method: stringParam(params, "method", http.MethodGet)}
	if !httpreq.KnownMethod(r.method) {
		return request{}, errBadMethod
	}
	headers, err := headersOf(params["headers"])
	if err != nil {
		return request{}, err
	}
	r.headers = headers
	if raw, ok := params["body"]; ok {
		body, isString := raw.(string)
		if !isString || body == "" || len(body) > httpreq.MaxBodyLen || !httpreq.TakesBody(r.method) || httpreq.BodyProblem(body) != "" {
			return request{}, errBadBody
		}
		r.body, r.hasBody = body, true
	}
	r.contentType = httpreq.DefaultContentType
	if raw, ok := params["content_type"]; ok {
		ct, isString := raw.(string)
		if !isString || !r.hasBody || len(ct) > httpreq.MaxContentTypeLen || httpreq.MediaTypeProblem(ct) != "" {
			return request{}, errBadBody
		}
		r.contentType = ct
	}
	return r, nil
}

// stringParam reads a string param, def when absent; a value of another
// type reads as "" so the caller refuses it.
func stringParam(params map[string]any, key, def string) string {
	v, ok := params[key]
	if !ok {
		return def
	}
	s, _ := v.(string)
	return s
}

// headersOf reads the headers param: absent is none; otherwise a list of
// at most httpreq.MaxHeaders strings, each an acceptable entry of at most
// httpreq.MaxHeaderLen bytes, no name twice.
func headersOf(raw any) ([][2]string, error) {
	if raw == nil {
		return nil, nil
	}
	var entries []string
	switch list := raw.(type) {
	case []string:
		entries = list
	case []any:
		for _, v := range list {
			s, ok := v.(string)
			if !ok {
				return nil, errBadHeader
			}
			entries = append(entries, s)
		}
	default:
		return nil, errBadHeader
	}
	if len(entries) > httpreq.MaxHeaders {
		return nil, errBadHeader
	}
	out := make([][2]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		name, value, ok := httpreq.ParseHeader(e)
		if !ok || len(e) > httpreq.MaxHeaderLen || seen[name] {
			return nil, errBadHeader
		}
		seen[name] = true
		out = append(out, [2]string{name, value})
	}
	return out, nil
}

// writes reports whether r may change something on its target
// (httpreq.Writes).
func (r request) writes() bool { return httpreq.Writes(r.method, r.hasBody) }

// headerNames are the names of r's plain headers.
func (r request) headerNames() []string {
	out := make([]string, len(r.headers))
	for i, h := range r.headers {
		out[i] = h[0]
	}
	return out
}

// bodyReader is the request body to hand http.NewRequest, nil for none. A
// strings.Reader lets net/http resend it on a 307 or 308.
func (r request) bodyReader() io.Reader {
	if !r.hasBody {
		return nil
	}
	return strings.NewReader(r.body)
}

// apply sets the headers and, with a body, its Content-Type on req. The
// check's credentials are applied after it, so the secret's header
// replaces a plain header of the same name.
func (r request) apply(req *http.Request) {
	for _, h := range r.headers {
		req.Header.Set(h[0], h[1])
	}
	if r.hasBody {
		req.Header.Set("Content-Type", r.contentType)
	}
}
