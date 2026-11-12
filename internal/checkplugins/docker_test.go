package checkplugins_test

import (
	"strings"
	"testing"
)

// TestValidateDockerParams, ported from internal/checks: the docker
// plugin's parameter shape (backlog 3.12) — selection, label and socket,
// nothing else. A label next to "all" is accepted: the manager validates
// shape only.
func TestValidateDockerParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"accepts empty", "docker", map[string]any{}, true},
		{"accepts nil", "docker", nil, true},
		{"accepts all", "docker", map[string]any{"selection": "all"}, true},
		{"accepts labeled", "docker", map[string]any{"selection": "labeled"}, true},
		{"accepts labeled with key only", "docker", map[string]any{"selection": "labeled", "label": "pulsix.monitor"}, true},
		{"accepts slash in key", "docker", map[string]any{"label": "com.example/watch=yes"}, true},
		{"accepts all with a label (opt-out key)", "docker", map[string]any{"selection": "all", "label": "pulsix.monitor=true"}, true},
		{"accepts empty value after =", "docker", map[string]any{"label": "k="}, true},
		{"accepts max key", "docker", map[string]any{"label": strings.Repeat("k", 255)}, true},
		{"accepts max value", "docker", map[string]any{"label": "k=" + strings.Repeat("v", 255)}, true},
		{"accepts unix socket", "docker", map[string]any{"socket": "/var/run/docker.sock"}, true},
		{"accepts podman socket", "docker", map[string]any{"socket": "/run/podman/podman.sock"}, true},
		{"accepts named pipe", "docker", map[string]any{"socket": "npipe:////./pipe/docker_engine"}, true},

		{"refuses unknown selection", "docker", map[string]any{"selection": "some"}, false},
		{"refuses selection not a string", "docker", map[string]any{"selection": 1}, false},
		{"refuses empty label", "docker", map[string]any{"label": ""}, false},
		{"refuses label not a string", "docker", map[string]any{"label": 1}, false},
		{"refuses empty key", "docker", map[string]any{"label": "=x"}, false},
		{"refuses space in key", "docker", map[string]any{"label": "bad key=x"}, false},
		{"refuses long value", "docker", map[string]any{"label": "k=" + strings.Repeat("v", 256)}, false},
		{"refuses control in value", "docker", map[string]any{"label": "k=a\nb"}, false},
		{"refuses long key", "docker", map[string]any{"label": strings.Repeat("k", 256)}, false},
		{"refuses empty socket", "docker", map[string]any{"socket": ""}, false},
		{"refuses socket not a string", "docker", map[string]any{"socket": 1}, false},
		{"refuses relative socket", "docker", map[string]any{"socket": "docker.sock"}, false},
		{"refuses tcp socket", "docker", map[string]any{"socket": "tcp://10.0.0.1:2375"}, false},
		{"refuses pipe without name", "docker", map[string]any{"socket": "npipe:////./pipe/"}, false},
		{"refuses nul in socket", "docker", map[string]any{"socket": "/a\x00b"}, false},
		{"refuses long socket", "docker", map[string]any{"socket": "/" + strings.Repeat("s", 255)}, false},
		{"refuses unknown parameter", "docker", map[string]any{"units": []any{"x"}}, false},
	})
}
