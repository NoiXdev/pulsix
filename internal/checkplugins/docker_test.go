package checkplugins_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
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

		{"accepts resources on", "docker", map[string]any{"resources": true}, true},
		{"accepts resources off", "docker", map[string]any{"resources": false}, true},
		{"refuses resources as a string", "docker", map[string]any{"resources": "true"}, false},
		{"refuses resources as a number", "docker", map[string]any{"resources": 1.0}, false},
	})
}

// dockerResourceMetrics are the metrics the resources option adds
// (backlog 3.12a).
var dockerResourceMetrics = []string{
	"docker.cpu_percent", "docker.memory_bytes", "docker.memory_percent",
	"docker.net_rx_bytes_per_s", "docker.net_tx_bytes_per_s",
	"docker.block_read_bytes_per_s", "docker.block_write_bytes_per_s",
	"docker.containers_unsampled",
}

// TestDockerAsksForResourceMetrics (backlog 3.12a): the usage metrics are
// asked only while resources is true, the agent's own reading of the
// parameter; switching it off reads their last values as no longer asked.
// The state metrics are always asked.
func TestDockerAsksForResourceMetrics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   bool
	}{
		{"resources on", map[string]any{"resources": true}, true},
		{"resources off", map[string]any{"resources": false}, false},
		{"resources absent", map[string]any{"selection": "all"}, false},
		{"no params", nil, false},
		{"resources not a bool", map[string]any{"resources": "true"}, false},
	} {
		for _, m := range dockerResourceMetrics {
			if got := checkplugins.AsksForMetric("docker", tc.params, nil, m); got != tc.want {
				t.Errorf("%s: AsksForMetric(%s) = %v, want %v", tc.name, m, got, tc.want)
			}
		}
		for _, m := range []string{"docker.state", "docker.health", "docker.restart_count", "docker.exit_code", "docker.containers_total", "docker.containers_running", "docker.containers_unreported"} {
			if !checkplugins.AsksForMetric("docker", tc.params, nil, m) {
				t.Errorf("%s: %s not asked", tc.name, m)
			}
		}
	}
	d, _ := checkplugins.Get("docker")
	declared := map[string]checkplugins.MetricKind{}
	for _, m := range d.Metrics {
		declared[m.Name] = m.Kind
	}
	for _, m := range dockerResourceMetrics {
		if declared[m] != checkplugins.Numeric {
			t.Errorf("%s declared as %q, want numeric", m, declared[m])
		}
	}
}
