package checkplugins

// dockerPlugin: container state through the local engine socket (backlog
// 3.12). Only the shape of the parameters is checked here; what a label
// means under each selection is the agent's business, so a label next to
// selection "all" is accepted. The agent trusts params that pass here, so
// these bounds and internal/agent/checks/plugins/docker/ agree.
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
		},
		Metrics: []MetricSpec{
			{Name: "docker.state", Kind: Discrete},
			{Name: "docker.health", Kind: Discrete},
			{Name: "docker.restart_count", Kind: Numeric},
			{Name: "docker.exit_code", Kind: Numeric},
			{Name: "docker.containers_running", Kind: Numeric},
			{Name: "docker.containers_total", Kind: Numeric},
		},
	}
}
