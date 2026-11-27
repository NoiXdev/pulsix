package secrets

import (
	"net/netip"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"
)

// maxDenyCIDRs bounds probes.deny_cidrs.
const maxDenyCIDRs = 256

// ProbePolicy is the probes section of the file (backlog 3.82): whether
// this agent probes other hosts for the manager at all, and the address
// ranges its probes must never reach. The zero value is the default:
// probing on, no deny list of its own.
type ProbePolicy struct {
	// Disabled is enabled: false — the agent reports that it does not
	// probe, so the manager assigns it nothing.
	Disabled bool
	// DenyCIDRs are the ranges the agent's dial guard refuses on top of
	// its built-in ones, as written (the guard masks and canonicalises).
	DenyCIDRs []netip.Prefix
}

func (p ProbePolicy) clone() ProbePolicy {
	p.DenyCIDRs = slices.Clone(p.DenyCIDRs)
	return p
}

const (
	badProbes      = "probes must map enabled and deny_cidrs to their values"
	badProbesKey   = "unknown key under probes; only enabled and deny_cidrs are allowed"
	badEnabled     = "enabled must be true or false"
	badDenyList    = "deny_cidrs must be a list of address ranges"
	badDenyEntry   = "a deny_cidrs entry must be an address range such as 10.0.0.0/8 or fd00::/8 (write /32 or /128 for one address)"
	tooManyDenyMsg = "more than 256 deny_cidrs entries"
)

// parseProbes decodes the mapping under probes:
//
//	probes:
//	  enabled: <true | false>
//	  deny_cidrs: [<CIDR>, …]
//
// Both keys are optional; a missing or empty section is the default.
// enabled must be a YAML boolean (a quoted "false" is refused, not read
// as text), each deny_cidrs entry a single CIDR. No reason quotes what
// was written.
func parseProbes(n *yaml.Node) (ProbePolicy, error) {
	if n == nil || isNull(n) {
		return ProbePolicy{}, nil
	}
	if n.Kind != yaml.MappingNode {
		return ProbePolicy{}, errAt(n, badProbes)
	}
	var out ProbePolicy
	lines := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || (key.Value != "enabled" && key.Value != "deny_cidrs") {
			return ProbePolicy{}, errAt(key, badProbesKey)
		}
		if line, dup := lines[key.Value]; dup {
			return ProbePolicy{}, errAt(key, key.Value+" is already set at line "+strconv.Itoa(line))
		}
		lines[key.Value] = key.Line
		switch key.Value {
		case "enabled":
			on, err := parseEnabled(key, val)
			if err != nil {
				return ProbePolicy{}, err
			}
			out.Disabled = !on
		case "deny_cidrs":
			deny, err := parseDenyCIDRs(key, val)
			if err != nil {
				return ProbePolicy{}, err
			}
			out.DenyCIDRs = deny
		}
	}
	return out, nil
}

// parseEnabled reads enabled's value; an empty one is reported at its key.
func parseEnabled(key, val *yaml.Node) (bool, error) {
	if isNull(val) {
		return false, errAt(key, badEnabled)
	}
	var on bool
	if val.Kind != yaml.ScalarNode || val.ShortTag() != "!!bool" || val.Decode(&on) != nil {
		return false, errAt(val, badEnabled)
	}
	return on, nil
}

// parseDenyCIDRs reads the deny_cidrs list; an empty one is none.
func parseDenyCIDRs(key, val *yaml.Node) ([]netip.Prefix, error) {
	if isNull(val) {
		return nil, nil
	}
	if val.Kind != yaml.SequenceNode {
		return nil, errAt(val, badDenyList)
	}
	if len(val.Content) > maxDenyCIDRs {
		return nil, errAt(key, tooManyDenyMsg)
	}
	var out []netip.Prefix
	for _, e := range val.Content {
		if e.Kind != yaml.ScalarNode || isNull(e) {
			return nil, errAt(e, badDenyEntry)
		}
		p, err := netip.ParsePrefix(e.Value)
		if err != nil {
			return nil, errAt(e, badDenyEntry)
		}
		out = append(out, p)
	}
	return out, nil
}
