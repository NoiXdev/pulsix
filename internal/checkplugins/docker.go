package checkplugins

import "slices"

// dockerPlugin: container state through the local engine socket (backlog
// 3.12), and with resources each container's usage (backlog 3.12a). Only
// the shape of the parameters is checked here; what a label means under
// each selection is the agent's business, so a label next to selection
// "all" is accepted. The agent trusts params that pass here, so these
// bounds and internal/agent/checks/plugins/docker/ agree.
func dockerPlugin() Descriptor {
	return Descriptor{
		Name: "docker",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "selection", Type: TypeEnum, Options: []string{"all", "labeled"}, Default: "all"},
			{Name: "label", Type: TypeString, Format: FormatDockerLabel, Default: "pulsix.monitor=true"},
			// The default is the Linux and macOS socket; on Windows the agent
			// uses npipe:////./pipe/docker_engine.
			{Name: "socket", Type: TypeString, MaxBytes: 255, Format: FormatDockerSocket, Default: "/var/run/docker.sock"},
			// Opt-in: reading each container's stats is the engine's most
			// expensive request, and the usage adds seven metrics per
			// container towards the per-host tuple cap.
			{Name: "resources", Type: TypeBool, Default: false},
		},
		MetricAsked: dockerMetricAsked,
		Metrics: []MetricSpec{
			{Name: "docker.state", Kind: Discrete},
			{Name: "docker.health", Kind: Discrete},
			{Name: "docker.restart_count", Kind: Numeric},
			{Name: "docker.exit_code", Kind: Numeric},
			{Name: "docker.containers_running", Kind: Numeric},
			{Name: "docker.containers_total", Kind: Numeric},
			// Containers left out of a round because their name is refused
			// as an item, reported on item "".
			{Name: "docker.containers_unreported", Kind: Numeric},
			// With resources, per container: CPU (100 is one core), memory
			// in use without the file cache and its share of the limit,
			// network and block I/O rates. A stopped container reads 0 on
			// what it reported while running.
			{Name: "docker.cpu_percent", Kind: Numeric},
			{Name: "docker.memory_bytes", Kind: Numeric},
			{Name: "docker.memory_percent", Kind: Numeric},
			{Name: "docker.net_rx_bytes_per_s", Kind: Numeric},
			{Name: "docker.net_tx_bytes_per_s", Kind: Numeric},
			{Name: "docker.block_read_bytes_per_s", Kind: Numeric},
			{Name: "docker.block_write_bytes_per_s", Kind: Numeric},
			// Containers over the agent's cap of 100 (running first, then
			// stopped ones it remembers) that carry no usage, on item "".
			{Name: "docker.containers_unsampled", Kind: Numeric},
		},
	}
}

// dockerResourceMetrics are reported only while resources is on.
var dockerResourceMetrics = []string{
	"docker.cpu_percent", "docker.memory_bytes", "docker.memory_percent",
	"docker.net_rx_bytes_per_s", "docker.net_tx_bytes_per_s",
	"docker.block_read_bytes_per_s", "docker.block_write_bytes_per_s",
	"docker.containers_unsampled",
}

// dockerMetricAsked: the usage metrics are asked only while resources is
// true — the agent's own reading of the parameter (a bool, false when
// absent); every other metric is asked.
func dockerMetricAsked(params map[string]any, metric string) bool {
	if !slices.Contains(dockerResourceMetrics, metric) {
		return true
	}
	on, _ := params["resources"].(bool)
	return on
}
