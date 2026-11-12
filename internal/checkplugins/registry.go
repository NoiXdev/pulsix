package checkplugins

// All returns every plugin's descriptor in the order the SPA offers them:
// the agent plugins, then the synthetic ones. Every call builds fresh
// values, so a caller may change what it gets without affecting anyone
// else. Plugins without parameters carry an empty, non-nil Params.
func All() []Descriptor {
	all := []Descriptor{
		cpuPlugin(), memoryPlugin(), diskPlugin(), servicePlugin(), networkPlugin(), uptimePlugin(),
		dockerPlugin(), temperaturePlugin(), smartPlugin(), raidPlugin(),
		icmpPlugin(), tcpPlugin(), httpPlugin(), tlsPlugin(), dnsPlugin(),
	}
	for i := range all {
		if all[i].Params == nil {
			all[i].Params = []ParamSpec{}
		}
	}
	return all
}

// Get returns the descriptor of the plugin called name.
func Get(name string) (Descriptor, bool) {
	for _, d := range All() {
		if d.Name == name {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Names returns every plugin's name, in All's order.
func Names() []string {
	all := All()
	names := make([]string, len(all))
	for i, d := range all {
		names[i] = d.Name
	}
	return names
}
