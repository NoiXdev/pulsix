package network

// excludedInterfacePrefixesFor lists the virtual-interface name prefixes the
// default interface set skips on goos.
func excludedInterfacePrefixesFor(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"lo", "utun", "awdl", "llw", "gif", "stf", "bridge", "anpi", "ap"}
	case "windows":
		return []string{"Loopback", "isatap", "Teredo", "6TO4", "Local Area Connection*"}
	default:
		return []string{
			"veth", "docker", "br-", "virbr", "cni", "flannel", "cali", "vxlan", "kube-", "tun", "tap",
			"vnet", "podman", "lxcbr", "lxdbr",
		}
	}
}
