package checkplugins

// Names returns every plugin's name in the order the SPA offers them: the
// agent plugins, then the synthetic ones. It is the one list of plugins;
// Get maps each name to its descriptor.
func Names() []string {
	return []string{
		"cpu", "memory", "disk", "service", "network", "uptime",
		"docker", "temperature", "smart", "raid",
		"icmp", "tcp", "http", "tls", "dns",
	}
}

// All returns every plugin's descriptor, in Names' order. Every call builds
// fresh values, so a caller may change what it gets without affecting
// anyone else. Plugins without parameters carry an empty, non-nil Params.
func All() []Descriptor {
	names := Names()
	all := make([]Descriptor, len(names))
	for i, name := range names {
		all[i], _ = Get(name)
	}
	return all
}

// Get returns the descriptor of the plugin called name, built fresh like
// All's. It builds only that one descriptor, so the manager can ask it on
// every resolved check (IsSynthetic, AsksForItem) without rebuilding the
// whole set.
func Get(name string) (Descriptor, bool) {
	var d Descriptor
	switch name {
	case "cpu":
		d = cpuPlugin()
	case "memory":
		d = memoryPlugin()
	case "disk":
		d = diskPlugin()
	case "service":
		d = servicePlugin()
	case "network":
		d = networkPlugin()
	case "uptime":
		d = uptimePlugin()
	case "docker":
		d = dockerPlugin()
	case "temperature":
		d = temperaturePlugin()
	case "smart":
		d = smartPlugin()
	case "raid":
		d = raidPlugin()
	case "icmp":
		d = icmpPlugin()
	case "tcp":
		d = tcpPlugin()
	case "http":
		d = httpPlugin()
	case "tls":
		d = tlsPlugin()
	case "dns":
		d = dnsPlugin()
	default:
		return Descriptor{}, false
	}
	if d.Params == nil {
		d.Params = []ParamSpec{}
	}
	return d, true
}
