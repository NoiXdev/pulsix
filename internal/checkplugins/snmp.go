package checkplugins

import (
	"errors"
	"fmt"
	"strings"
)

// snmp plugin bounds (backlog 3.13d). internal/probe/snmp trusts params
// that pass here, so the two must agree.
const (
	maxSNMPOIDs        = 32
	defSNMPPort        = 161
	maxSNMPTimeoutSecs = 30
	defSNMPTimeoutSecs = 5
)

// snmpPlugin: poll a device over SNMP from the Pulsix server (backlog
// 3.13d, spec docs/superpowers/specs/2026-11-14-snmp-checks-design.md).
// Its credentials are a secret in the organization's secret store, named
// by the secret parameter; the manager never puts them in params. Each
// oids entry "name=OID" reports its value on item "name"; interfaces
// reports the device's IF-MIB table on one item per interface, named by
// the device; snmp.up, snmp.response_ms and snmp.uptime_seconds are on
// the whole-host item "".
func snmpPlugin() Descriptor {
	secret := SecretParam()
	secret.Required = true
	secret.Example = "switch-core"
	return Descriptor{
		Name: "snmp",
		Runs: Server,
		Params: []ParamSpec{
			secret,
			{Name: "version", Type: TypeEnum, Options: []string{"v2c", "v3"}, Default: "v2c"},
			syntheticPort(false, int64(defSNMPPort)),
			{Name: "timeout_seconds", Type: TypeInteger, Min: bound(1), Max: bound(maxSNMPTimeoutSecs), Default: int64(defSNMPTimeoutSecs)},
			{Name: "oids", Type: TypeList, MaxEntries: maxSNMPOIDs, Format: FormatSNMPOID, Example: "ups_load=1.3.6.1.2.1.33.1.4.4.1.5.1"},
			{Name: "interfaces", Type: TypeBool, Default: false},
		},
		ItemParam:           "oids",
		HostItemAlwaysAsked: true,
		ItemAsked:           snmpItemAsked,
		Check:               snmpCheck,
		Metrics: []MetricSpec{
			{Name: "snmp.up", Kind: Numeric},
			{Name: "snmp.response_ms", Kind: Numeric},
			{Name: "snmp.uptime_seconds", Kind: Numeric},
			// Item: an oids entry's name.
			{Name: "snmp.value", Kind: Numeric},
			{Name: "snmp.text", Kind: Discrete},
			// Item: an interface, named by the device.
			{Name: "snmp.if.status", Kind: Discrete},
			{Name: "snmp.if.in_bps", Kind: Numeric},
			{Name: "snmp.if.out_bps", Kind: Numeric},
			{Name: "snmp.if.errors", Kind: Numeric},
		},
	}
}

// snmpOIDName is the item an oids entry reports on: its name, before the
// first '=' (FormatSNMPOID's split; a name never contains one).
func snmpOIDName(entry string) string {
	name, _, _ := strings.Cut(entry, "=")
	return name
}

// snmpItemAsked: with interfaces set, every item is asked for (interface
// names are the device's, unknown to the manager); otherwise only the
// names of the oids entries are.
func snmpItemAsked(params map[string]any, entries []string, item string) bool {
	if params["interfaces"] == true {
		return true
	}
	for _, e := range entries {
		if snmpOIDName(e) == item {
			return true
		}
	}
	return false
}

// snmpCheck: a check must poll something, and two oids entries must not
// report on the same item.
func snmpCheck(params map[string]any) error {
	entries, _ := stringList(params["oids"])
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := snmpOIDName(e)
		if seen[name] {
			return fmt.Errorf("oids names %q twice", name)
		}
		seen[name] = true
	}
	if len(entries) == 0 && params["interfaces"] != true {
		return errors.New("nothing to poll: list at least one entry in oids or set interfaces to true")
	}
	return nil
}
