package checkplugins

import (
	"errors"
	"fmt"

	"github.com/noixdev/pulsix/internal/probe/assert"
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
//
// Backlog 3.134 added assertions (plan
// docs/superpowers/plans/2026-11-26-snmp-assertions-3.134.md): each entry
// "name=expression [@warn]" in the snmp dialect — value("name") of an oids
// entry, response_ms, uptime — reports on item "name" whether it held
// (snmp.assertion) and what was seen (snmp.assertion_actual). oids stays
// the ItemParam; the assertion names share its items' namespace.
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
			{Name: "assertions", Type: TypeList, MaxEntries: assert.MaxEntries, MaxBytes: assert.MaxEntryLen, Format: FormatSNMPAssertion, Example: snmpAssertionExample},
		},
		ItemParam:           "oids",
		HostItemAlwaysAsked: true,
		ItemAsked:           snmpItemAsked,
		Check:               snmpCheck,
		Metrics: []MetricSpec{
			{Name: "snmp.up", Kind: Numeric},
			// The failure reason of an attempt (probe.Failure*): "none" while
			// up, else why it failed.
			{Name: "snmp.failure", Kind: Discrete},
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
			// Item: an assertions entry's name. pass, or the entry's
			// severity (warn, crit) when it failed; and what was seen.
			{Name: "snmp.assertion", Kind: Discrete},
			{Name: "snmp.assertion_actual", Kind: Discrete},
		},
	}
}

// snmpAssertionExample is a valid snmp assertion, reading the oids
// parameter's example entry.
const snmpAssertionExample = `busy=value("ups_load") < 80`

// snmpItemAsked: with interfaces set, every item is asked for (interface
// names are the device's, unknown to the manager); otherwise only the
// names of the oids and assertions entries are.
func snmpItemAsked(params map[string]any, entries []string, item string) bool {
	if params["interfaces"] == true {
		return true
	}
	assertions, _ := stringList(params["assertions"])
	return namedEntryAsked(entries, item) || namedEntryAsked(assertions, item)
}

// snmpCheck: a check must poll something, no two oids or assertions
// entries may report on the same item, and an assertion's value("x") must
// read an oids entry x.
func snmpCheck(params map[string]any) error {
	entries, _ := stringList(params["oids"])
	if name, twice := duplicateEntryName(entries); twice {
		return fmt.Errorf("oids names %q twice", name)
	}
	if len(entries) == 0 && params["interfaces"] != true {
		return errors.New("nothing to poll: list at least one entry in oids or set interfaces to true")
	}
	assertions, _ := stringList(params["assertions"])
	if name, twice := duplicateEntryName(assertions); twice {
		return fmt.Errorf("assertions names %q twice", name)
	}
	for _, entry := range assertions {
		if namedEntryAsked(entries, entryName(entry)) {
			return fmt.Errorf("oids and assertions both name %q", entryName(entry))
		}
		// Every entry passed its format already.
		a, err := assert.SNMP.Parse(entry)
		if err == nil && a.Subject() == assert.SubjectValue && !namedEntryAsked(entries, a.Arg()) {
			return fmt.Errorf("assertion %q reads value(%s), which no oids entry names", a.Name(), assert.Quote(a.Arg()))
		}
	}
	return nil
}
