package checks_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
)

// TestIsSynthetic: the five probe plugins run on the manager, everything
// else on the host's agent.
func TestIsSynthetic(t *testing.T) {
	for _, p := range []string{checks.PluginICMP, checks.PluginTCP, checks.PluginHTTP, checks.PluginTLS, checks.PluginDNS} {
		if !checks.IsSynthetic(p) {
			t.Errorf("IsSynthetic(%q) = false, want true", p)
		}
	}
	for _, p := range []string{checks.PluginCPU, checks.PluginMemory, checks.PluginDisk, checks.PluginService,
		checks.PluginNetwork, checks.PluginUptime, "", "snmp"} {
		if checks.IsSynthetic(p) {
			t.Errorf("IsSynthetic(%q) = true, want false", p)
		}
	}
}

// TestDockerIsAgentSide: the docker plugin runs on the host's agent, so the
// resolver must not treat it as a manager-side probe.
func TestDockerIsAgentSide(t *testing.T) {
	if checks.IsSynthetic(checks.PluginDocker) {
		t.Fatal("IsSynthetic(docker) = true, want false")
	}
}

// TestHardwarePluginsAreAgentSide: the three plugins run on the host's agent.
func TestHardwarePluginsAreAgentSide(t *testing.T) {
	for _, p := range []string{checks.PluginTemperature, checks.PluginSmart, checks.PluginRaid} {
		if checks.IsSynthetic(p) {
			t.Fatalf("IsSynthetic(%s) = true, want false", p)
		}
	}
}

// TestPluginConstantsAreTheDescriptors: the Plugin constants are spellings
// of the descriptors' names, not a list of their own — every descriptor has
// one, in the descriptors' order, and no constant names anything else.
func TestPluginConstantsAreTheDescriptors(t *testing.T) {
	constants := []string{
		checks.PluginCPU, checks.PluginMemory, checks.PluginDisk, checks.PluginService,
		checks.PluginNetwork, checks.PluginUptime, checks.PluginDocker,
		checks.PluginTemperature, checks.PluginSmart, checks.PluginRaid, checks.PluginPostgres, checks.PluginMySQL, checks.PluginRedis, checks.PluginNginx, checks.PluginApache, checks.PluginCaddy,
		checks.PluginICMP, checks.PluginTCP, checks.PluginHTTP, checks.PluginTLS, checks.PluginDNS,
	}
	if names := checkplugins.Names(); !slices.Equal(constants, names) {
		t.Fatalf("Plugin constants %v, descriptors %v", constants, names)
	}
}

// TestUnsupportedPluginMessage pins the refusal of an unknown plugin as it
// read when the list was a literal, before it was derived from the
// descriptors (backlog 3.54).
func TestUnsupportedPluginMessage(t *testing.T) {
	_, err := checks.ValidateDefinitionShape("snmp", "", nil, time.Minute)
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("ValidateDefinitionShape(snmp) = %v, want ErrInvalidInput", err)
	}
	const want = `invalid input: plugin must be one of cpu, memory, disk, service, network, uptime, docker, temperature, smart, raid, postgres, mysql, redis, nginx, apache, caddy, icmp, tcp, http, tls, dns, got "snmp"`
	if err.Error() != want {
		t.Fatalf("ValidateDefinitionShape(snmp) = %q, want %q", err.Error(), want)
	}
}
