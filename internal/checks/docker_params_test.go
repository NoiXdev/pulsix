package checks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

// TestValidateDockerParams pins the docker plugin's parameter shape
// (backlog 3.12): selection, label and socket, nothing else. The opt-out
// semantics of label under selection "all" belong to the agent; the manager
// only validates shape, so a label next to "all" is accepted.
func TestValidateDockerParams(t *testing.T) {
	accepted := []struct {
		name   string
		params map[string]any
	}{
		{"empty", map[string]any{}},
		{"nil", nil},
		{"all", map[string]any{"selection": "all"}},
		{"labeled", map[string]any{"selection": "labeled"}},
		{"labeled with key only", map[string]any{"selection": "labeled", "label": "pulsix.monitor"}},
		{"slash in key", map[string]any{"label": "com.example/watch=yes"}},
		{"all with a label (opt-out key)", map[string]any{"selection": "all", "label": "pulsix.monitor=true"}},
		{"empty value after =", map[string]any{"label": "k="}},
		{"max key", map[string]any{"label": strings.Repeat("k", 255)}},
		{"max value", map[string]any{"label": "k=" + strings.Repeat("v", 255)}},
		{"unix socket", map[string]any{"socket": "/var/run/docker.sock"}},
		{"podman socket", map[string]any{"socket": "/run/podman/podman.sock"}},
		{"named pipe", map[string]any{"socket": "npipe:////./pipe/docker_engine"}},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			if _, err := checks.ValidateDefinitionShape("docker", "docker", tc.params, time.Minute); err != nil {
				t.Fatalf("ValidateDefinitionShape(docker, %v) = %v, want nil", tc.params, err)
			}
		})
	}
	refused := []struct {
		name   string
		params map[string]any
	}{
		{"unknown selection", map[string]any{"selection": "some"}},
		{"selection not a string", map[string]any{"selection": 1}},
		{"empty label", map[string]any{"label": ""}},
		{"label not a string", map[string]any{"label": 1}},
		{"empty key", map[string]any{"label": "=x"}},
		{"space in key", map[string]any{"label": "bad key=x"}},
		{"long value", map[string]any{"label": "k=" + strings.Repeat("v", 256)}},
		{"control in value", map[string]any{"label": "k=a\nb"}},
		{"long key", map[string]any{"label": strings.Repeat("k", 256)}},
		{"empty socket", map[string]any{"socket": ""}},
		{"socket not a string", map[string]any{"socket": 1}},
		{"relative socket", map[string]any{"socket": "docker.sock"}},
		{"tcp socket", map[string]any{"socket": "tcp://10.0.0.1:2375"}},
		{"pipe without name", map[string]any{"socket": "npipe:////./pipe/"}},
		{"nul in socket", map[string]any{"socket": "/a\x00b"}},
		{"long socket", map[string]any{"socket": "/" + strings.Repeat("s", 255)}},
		{"unknown parameter", map[string]any{"units": []any{"x"}}},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			_, err := checks.ValidateDefinitionShape("docker", "docker", tc.params, time.Minute)
			if !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("ValidateDefinitionShape(docker, %v) = %v, want ErrInvalidInput", tc.params, err)
			}
		})
	}
}

// TestDockerIsAgentSide: the docker plugin runs on the host's agent, so the
// resolver must not treat it as a manager-side probe.
func TestDockerIsAgentSide(t *testing.T) {
	if checks.IsSynthetic(checks.PluginDocker) {
		t.Fatal("IsSynthetic(docker) = true, want false")
	}
}
