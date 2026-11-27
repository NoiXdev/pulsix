package notifytest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/noixdev/pulsix/internal/notify"
)

// Body is the body of the one JSON request a chat preset renders (backlog
// 4.7's presets), an error when it rendered anything else.
func Body(reqs []notify.Request, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	if len(reqs) != 1 || reqs[0].Path != "" || reqs[0].ContentType != "" || len(reqs[0].Header) > 0 {
		return nil, fmt.Errorf("want one plain JSON request, got %+v", reqs)
	}
	return reqs[0].Body, nil
}

// dumped is one request as a golden file shows it.
type dumped struct {
	Key         string            `json:"key,omitempty"`
	Path        string            `json:"path,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
	Header      [][2]string       `json:"header,omitempty"`
	JSON        json.RawMessage   `json:"json,omitempty"`
	Form        map[string]string `json:"form,omitempty"`
}

// Dump renders a preset's requests for a golden file: a JSON array with
// each request's path, content type, headers and body — a JSON body as
// JSON, a form body as an object of its fields (backlog 4.8).
func Dump(reqs []notify.Request, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	out := make([]dumped, len(reqs))
	for i, r := range reqs {
		d := dumped{Key: r.Key, Path: r.Path, ContentType: r.ContentType, Header: r.Header}
		switch r.ContentType {
		case "", "application/json":
			if !json.Valid(r.Body) {
				return nil, fmt.Errorf("request %d: body is not JSON: %s", i+1, r.Body)
			}
			d.JSON = r.Body
		case "application/x-www-form-urlencoded":
			v, err := url.ParseQuery(string(r.Body))
			if err != nil {
				return nil, fmt.Errorf("request %d: %w", i+1, err)
			}
			d.Form = map[string]string{}
			for k := range v {
				d.Form[k] = v.Get(k)
			}
		default:
			return nil, fmt.Errorf("request %d: unexpected content type %q", i+1, r.ContentType)
		}
		out[i] = d
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
