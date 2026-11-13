package checkplugins

// plugin is one entry of the registry: a plugin's name and the constructor
// of its descriptor. The name is spelled next to the constructor so Get can
// find a plugin without building every descriptor; TestGetEveryName checks
// that each constructor builds the descriptor carrying its entry's name.
type plugin struct {
	name  string
	build func() Descriptor
}

// registry is the one list of plugins, in the order the SPA offers them:
// the agent plugins, then the synthetic ones. Names, Get and All all read
// it, so a plugin cannot be known to one and missing from another.
// Registering a plugin is adding its entry here. It is a func rather than a
// package var so nothing can change the list for every other caller.
func registry() []plugin {
	return []plugin{
		{"cpu", cpuPlugin},
		{"memory", memoryPlugin},
		{"disk", diskPlugin},
		{"service", servicePlugin},
		{"network", networkPlugin},
		{"uptime", uptimePlugin},
		{"docker", dockerPlugin},
		{"temperature", temperaturePlugin},
		{"smart", smartPlugin},
		{"raid", raidPlugin},
		{"postgres", postgresPlugin},
		{"mysql", mysqlPlugin},
		{"redis", redisPlugin},
		{"nginx", nginxPlugin},
		{"icmp", icmpPlugin},
		{"tcp", tcpPlugin},
		{"http", httpPlugin},
		{"tls", tlsPlugin},
		{"dns", dnsPlugin},
	}
}

// Names returns every plugin's name in the registry's order.
func Names() []string {
	reg := registry()
	names := make([]string, len(reg))
	for i, p := range reg {
		names[i] = p.name
	}
	return names
}

// All returns every plugin's descriptor, in Names' order. Every call builds
// fresh values, so a caller may change what it gets without affecting
// anyone else. Plugins without parameters carry an empty, non-nil Params.
func All() []Descriptor {
	reg := registry()
	all := make([]Descriptor, len(reg))
	for i, p := range reg {
		all[i] = p.descriptor()
	}
	return all
}

// Get returns the descriptor of the plugin called name, built fresh like
// All's. It finds the entry by name and builds only that one descriptor,
// so the manager can ask it on every resolved check (IsSynthetic,
// AsksForItem) without rebuilding the whole set.
func Get(name string) (Descriptor, bool) {
	for _, p := range registry() {
		if p.name == name {
			return p.descriptor(), true
		}
	}
	return Descriptor{}, false
}

func (p plugin) descriptor() Descriptor {
	d := p.build()
	if d.Params == nil {
		d.Params = []ParamSpec{}
	}
	return d
}
