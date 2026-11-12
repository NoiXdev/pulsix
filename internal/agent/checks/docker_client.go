package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The docker check reads container state from the Docker Engine API over a
// local socket with plain GETs; no SDK is involved.
//
// apiPrefix pins API v1.41 (Docker 20.10+). Podman's Docker-compatible API
// serves it too, and a pinned version keeps the answer shape stable across
// engine upgrades.
const (
	apiPrefix = "/v1.41"

	// maxListBody and maxInspectBody bound what is read from the socket, so
	// something that is not Docker (or a runaway engine) cannot exhaust memory.
	maxListBody    = 16 << 20
	maxInspectBody = 4 << 20

	shortIDLen = 12
)

// errContainerGone is Inspect's answer to a 404: the container vanished
// between the list and the inspect.
var errContainerGone = errors.New("container gone")

// containerSummary is one row of the container list.
type containerSummary struct {
	ID     string
	Name   string // first entry of Names, leading "/" cut
	State  string // the list's State, lower-case
	Labels map[string]string
}

// containerDetail is the part of an inspect the check reports.
type containerDetail struct {
	Health       string // "healthy", "unhealthy", "starting", or "none" when State.Health is absent
	RestartCount int
	ExitCode     int
}

// dockerEngine is the seam the docker plugin depends on; tests fake it.
type dockerEngine interface {
	// List returns all containers, running or not. labelFilter is a Docker
	// label filter ("key" or "key=value"); "" lists everything.
	List(ctx context.Context, labelFilter string) ([]containerSummary, error)
	// Inspect returns the detail of one container, or errContainerGone.
	Inspect(ctx context.Context, id string) (containerDetail, error)
}

type dockerClient struct {
	socket string
	http   *http.Client
}

// newDockerEngine returns a client for the engine at socket; "" means the
// platform default.
func newDockerEngine(socket string) (dockerEngine, error) {
	if socket == "" {
		socket = defaultDockerSocket
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := dialDocker(ctx, socket)
			if err != nil {
				return nil, fmt.Errorf("docker: cannot reach %s: %w", socket, err)
			}
			return c, nil
		},
		DisableKeepAlives: true,
	}
	return &dockerClient{socket: socket, http: &http.Client{Transport: tr}}, nil
}

func (c *dockerClient) List(ctx context.Context, labelFilter string) ([]containerSummary, error) {
	q := url.Values{"all": {"1"}}
	if labelFilter != "" {
		f, err := json.Marshal(map[string][]string{"label": {labelFilter}})
		if err != nil {
			return nil, fmt.Errorf("docker: list containers: %w", err)
		}
		q.Set("filters", string(f))
	}
	body, err := c.get(ctx, "/containers/json?"+q.Encode(), maxListBody, "list containers")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     string
		Names  []string
		State  string
		Labels map[string]string
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("docker: list containers: malformed response")
	}
	out := make([]containerSummary, 0, len(raw))
	for _, r := range raw {
		out = append(out, containerSummary{
			ID:     r.ID,
			Name:   containerName(r.ID, r.Names),
			State:  strings.ToLower(r.State),
			Labels: r.Labels,
		})
	}
	return out, nil
}

// containerName is the first name without its leading "/". A container the
// engine lists without any name is reported by the first 12 characters of its
// ID, the short ID docker itself shows.
func containerName(id string, names []string) string {
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}

func (c *dockerClient) Inspect(ctx context.Context, id string) (containerDetail, error) {
	body, err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/json", maxInspectBody, "inspect container")
	if err != nil {
		return containerDetail{}, err
	}
	// Only the needed fields are decoded; Config.Env never is.
	var raw struct {
		RestartCount int
		State        struct {
			ExitCode int
			Health   *struct{ Status string }
		}
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return containerDetail{}, errors.New("docker: inspect container: malformed response")
	}
	health := "none"
	if raw.State.Health != nil && raw.State.Health.Status != "" {
		health = raw.State.Health.Status
	}
	return containerDetail{Health: health, RestartCount: raw.RestartCount, ExitCode: raw.State.ExitCode}, nil
}

// get performs one GET and returns the body, refusing one over limit. Errors
// name the status, never the body.
func (c *dockerClient) get(ctx context.Context, path string, limit int64, what string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+apiPrefix+path, nil)
	if err != nil {
		return nil, fmt.Errorf("docker: %s: %w", what, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL; the dial error already names the socket
		}
		if strings.HasPrefix(err.Error(), "docker: ") {
			return nil, err
		}
		return nil, fmt.Errorf("docker: %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound && what == "inspect container" {
		return nil, errContainerGone
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker: %s: status %d", what, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("docker: %s: read response: %w", what, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("docker: %s: response over %d bytes", what, limit)
	}
	return body, nil
}
