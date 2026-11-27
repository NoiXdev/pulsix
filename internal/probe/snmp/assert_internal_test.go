package snmp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe/judge"
)

// assertionDevice answers sysUpTime and one value of each kind the
// assertions read.
func assertionDevice() *fakeDevice {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(360000)) // 3600 s
	dev.set(".1.3.6.1.9.1", gosnmp.Gauge32, uint(42))
	dev.set(".1.3.6.1.9.2", gosnmp.OctetString, []byte("online\x00\x00"))
	dev.set(".1.3.6.1.9.3", gosnmp.OctetString, []byte(" 0.15 "))
	dev.set(".1.3.6.1.9.4", gosnmp.Null, nil)
	dev.set(".1.3.6.1.9.5", gosnmp.OctetString, []byte(strings.Repeat("x", 600)+"\"tail"))
	dev.set(".1.3.6.1.9.6", gosnmp.OctetString, []byte{})
	dev.set(".1.3.6.1.9.7", gosnmp.OpaqueFloat, float32(0.15))
	dev.set(".1.3.6.1.9.8", gosnmp.Counter64, uint64(18446744073709551615))
	dev.set(".1.3.6.1.9.9", gosnmp.TimeTicks, uint32(360000))
	dev.rtt = 12 * time.Millisecond
	return dev
}

var assertionOIDs = []any{
	"load=1.3.6.1.9.1", "state=1.3.6.1.9.2", "la=1.3.6.1.9.3", "null=1.3.6.1.9.4",
	"missing=1.3.6.1.9.99", "long=1.3.6.1.9.5", "empty=1.3.6.1.9.6", "bad=3.1",
	"temp=1.3.6.1.9.7", "octets=1.3.6.1.9.8", "ticks=1.3.6.1.9.9",
}

// TestSNMPValueAssertions (backlog 3.134, plan R2–R5): value("name")
// compares numbers or text as the assertion says, by the value the device
// sent; text that is a number compares as one, text that is not is an
// error; a missing object fails every operator as absent; any other value
// error is the assertion's error.
func TestSNMPValueAssertions(t *testing.T) {
	cases := []struct {
		entry, verdict, actual, err string
	}{
		{`a=value("load") < 80`, "pass", "42", ""},
		{`a=value("load") > 80 @warn`, "warn", "42", ""},
		{`a=value("load") in 40..50`, "pass", "42", ""},
		{`a=value("load") == 42`, "pass", "42", ""},
		{`a=value("load") == "42"`, "pass", "42", ""},
		{`a=value("load") matches "^4[0-9]$"`, "pass", "42", ""},
		{`a=value("load") contains "7"`, "crit", "42", ""},
		{`a=value("state") == "online"`, "pass", `"online"`, ""},
		{`a=value("state") != "online"`, "crit", `"online"`, ""},
		{`a=value("state") matches "^on"`, "pass", `"online"`, ""},
		{`a=value("state") < 3`, "", "", errNotANumber},
		{`a=value("state") == 3`, "", "", errNotANumber},
		{`a=value("la") < 1`, "pass", `"0.15"`, ""},
		{`a=value("la") == "0.15"`, "pass", `"0.15"`, ""},
		{`a=value("empty") == ""`, "pass", `""`, ""},
		{`a=value("empty") < 1`, "", "", errNotANumber},
		// Compared as shown: cleaned, the first 512 bytes (R3).
		{`a=value("long") contains "tail"`, "crit", `"` + strings.Repeat("x", 126) + `"`, ""},
		{`a=value("long") contains "xxx"`, "pass", `"` + strings.Repeat("x", 126) + `"`, ""},
		{`a=value("missing") < 1`, "crit", judge.ActualAbsent, ""},
		{`a=value("missing") != "x"`, "crit", judge.ActualAbsent, ""},
		{`a=value("missing") !contains "x" @warn`, "warn", judge.ActualAbsent, ""},
		{`a=value("null") < 1`, "", "", errUnsupportedType},
		{`a=value("bad") < 1`, "", "", errBadOID},
		{`a=value("nowhere") < 1`, "", "", errNoSuchEntry},
		// An OpaqueFloat is widened through its shortest float32 spelling
		// (review M1): 0.15 is 0.15, not 0.15000000596046448.
		{`a=value("temp") == 0.15`, "pass", "0.15", ""},
		{`a=value("temp") <= 0.15`, "pass", "0.15", ""},
		{`a=value("temp") == "0.15"`, "pass", "0.15", ""},
		// A Counter64 keeps its exact decimal spelling for text and the
		// actual (review M2); numeric comparisons are float64's.
		{`a=value("octets") == "18446744073709551615"`, "pass", "18446744073709551615", ""},
		{`a=value("octets") matches "615$"`, "pass", "18446744073709551615", ""},
		{`a=value("octets") > 1`, "pass", "18446744073709551615", ""},
		// A TimeTicks oid reads hundredths of a second, as snmp.value
		// holds it (review M3); uptime is in seconds.
		{`a=value("ticks") == 360000`, "pass", "360000", ""},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			obs, _ := poll(newFakePoller(assertionDevice()), params(map[string]any{
				"oids": assertionOIDs, "assertions": []any{tc.entry},
			}), v2cSecret)
			m := byKey(t, obs)
			if tc.err != "" {
				wantErr(t, m, "snmp.assertion", "a", tc.err)
				wantErr(t, m, "snmp.assertion_actual", "a", tc.err)
				return
			}
			wantLabel(t, m, "snmp.assertion", "a", tc.verdict)
			wantLabel(t, m, "snmp.assertion_actual", "a", tc.actual)
		})
	}
}

// TestSNMPHostAssertions: response_ms is the first request's round trip,
// uptime the device's sysUpTime in seconds; a device without sysUpTime
// fails an uptime assertion as absent.
func TestSNMPHostAssertions(t *testing.T) {
	asserts := []any{"r=response_ms < 20", "u=uptime >= 3600", "slow=response_ms < 5 @warn", `x=nonsense`}
	obs, _ := poll(newFakePoller(assertionDevice()), params(map[string]any{"oids": assertionOIDs, "assertions": asserts}), v2cSecret)
	m := byKey(t, obs)
	wantLabel(t, m, "snmp.assertion", "r", "pass")
	wantLabel(t, m, "snmp.assertion_actual", "r", "12")
	wantLabel(t, m, "snmp.assertion", "u", "pass")
	wantLabel(t, m, "snmp.assertion_actual", "u", "3600")
	wantLabel(t, m, "snmp.assertion", "slow", "warn")
	wantErr(t, m, "snmp.assertion", "x", judge.ErrInvalid)

	dev := assertionDevice()
	delete(dev.values, oidSysUpTime)
	obs, _ = poll(newFakePoller(dev), params(map[string]any{"oids": assertionOIDs, "assertions": asserts}), v2cSecret)
	m = byKey(t, obs)
	wantLabel(t, m, "snmp.assertion", "u", "crit")
	wantLabel(t, m, "snmp.assertion_actual", "u", judge.ActualAbsent)
}

// TestSNMPAssertionsOnlyOnAnUpPoll (R8): a device that does not answer
// reports nothing on the assertions; a later request that goes unanswered
// fails only the values it asked for, and their assertions with them.
func TestSNMPAssertionsOnlyOnAnUpPoll(t *testing.T) {
	dev := assertionDevice()
	dev.getErr = errFakeTimeout
	obs, _ := poll(newFakePoller(dev), params(map[string]any{"oids": assertionOIDs, "assertions": []any{"u=uptime > 1"}}), v2cSecret)
	m := byKey(t, obs)
	wantValue(t, m, "snmp.up", "", 0)
	wantAbsent(t, m, "snmp.assertion", "u")

	dev = assertionDevice()
	dev.getErr, dev.getOK = errFakeTimeout, 1
	obs, _ = poll(newFakePoller(dev), params(map[string]any{"oids": numberedOIDs(20), "assertions": []any{`late=value("v19") < 1`, "u=uptime > 1"}}), v2cSecret)
	m = byKey(t, obs)
	wantErr(t, m, "snmp.assertion", "late", errNoAnswer)
	wantLabel(t, m, "snmp.assertion", "u", "pass")
}

// TestSNMPAssertionsNeverSeeTheSecret (R5): with a v2c community or v3
// credentials, whatever the assertions read and however they fail, no
// observation carries a secret's value.
func TestSNMPAssertionsNeverSeeTheSecret(t *testing.T) {
	v3 := map[string]string{
		"username": "USERNAMEVALUE", "auth_protocol": "SHA", "auth_password": "AUTHPASSWORDVALUE",
		"priv_protocol": "AES", "priv_password": "PRIVPASSWORDVALUE",
	}
	asserts := []any{
		`a=value("state") == "x"`, `b=value("state") < 1`, `c=value("load") matches "."`, `d=value("missing") < 1`,
		`e=value("null") < 1`, `f=value("nowhere") == "x"`, "g=uptime < 1", "h=response_ms > 1",
	}
	for name, tc := range map[string]struct {
		version string
		secret  map[string]string
		getErr  error
	}{
		"v2c":             {"v2c", v2cSecret, nil},
		"v3":              {"v3", v3, nil},
		"v3 later errors": {"v3", v3, errors.New("wrong digest AUTHPASSWORDVALUE")},
		"v2c late errors": {"v2c", v2cSecret, errors.New("timeout for " + community)},
	} {
		t.Run(name, func(t *testing.T) {
			dev := assertionDevice()
			if tc.getErr != nil {
				dev.getErr, dev.getOK = tc.getErr, 1
			}
			obs, _ := poll(newFakePoller(dev), params(map[string]any{
				"version": tc.version, "oids": append(numberedOIDs(18), assertionOIDs...), "assertions": asserts,
			}), tc.secret)
			if len(obs) == 0 {
				t.Fatal("no observations")
			}
			for _, o := range obs {
				text := o.Err
				if o.Label != nil {
					text += *o.Label
				}
				for _, v := range tc.secret {
					if len(v) > 3 && strings.Contains(text, v) {
						t.Fatalf("%s[%s] carries a secret value: %+v", o.Metric, o.Item, o)
					}
				}
			}
		})
	}
}

// TestSNMPFailAllCoversAssertions (R8): a poll that cannot be attempted
// puts its error on both metrics of every assertion, as Poll does for a
// secret it cannot use.
func TestSNMPFailAllCoversAssertions(t *testing.T) {
	p := params(map[string]any{"oids": []any{"a=1.3.6.1.9.1"}, "assertions": []any{`x=value("a") < 1`, "y=uptime > 1"}})
	failed := FailAll(p, "boom")
	m := byKey(t, failed)
	for _, item := range []string{"x", "y"} {
		wantErr(t, m, "snmp.assertion", item, "boom")
		wantErr(t, m, "snmp.assertion_actual", item, "boom")
	}
	polled, _ := poll(newFakePoller(assertionDevice()), p, nil)
	if len(polled) != len(failed) {
		t.Fatalf("Poll without a secret gave %+v, FailAll %+v", polled, failed)
	}
	for i := range polled {
		if polled[i].Metric != failed[i].Metric || polled[i].Item != failed[i].Item || polled[i].Err == "" {
			t.Fatalf("observation %d: Poll %+v, FailAll %+v", i, polled[i], failed[i])
		}
	}
}

// numberedOIDs are n entries v0, v1, … the fake device answers as
// gauges.
func numberedOIDs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("v%d=1.3.6.1.8.%d", i, i)
	}
	return out
}

// TestReadValueSpellsNumbersExactly (review M1, M2): snmp.value of an
// OpaqueFloat is its float32 spelling read as a float64, and a value
// keeps the decimal spelling of the number the device sent.
func TestReadValueSpellsNumbersExactly(t *testing.T) {
	for _, tc := range []struct {
		typ     gosnmp.Asn1BER
		v       any
		num     float64
		spelled string
	}{
		{gosnmp.OpaqueFloat, float32(0.15), 0.15, "0.15"},
		{gosnmp.OpaqueFloat, float32(-2.5e-7), -2.5e-7, "-0.00000025"},
		{gosnmp.OpaqueDouble, 0.1, 0.1, "0.1"},
		{gosnmp.Counter64, uint64(18446744073709551615), 18446744073709551615, "18446744073709551615"},
		{gosnmp.Integer, -42, -42, "-42"},
		{gosnmp.Gauge32, uint(77), 77, "77"},
	} {
		v := readValue(gosnmp.SnmpPDU{Type: tc.typ, Value: tc.v})
		if v.err != "" || v.isText || v.num != tc.num || v.spelled != tc.spelled {
			t.Errorf("readValue(%v %v) = %+v, want %v spelled %q", tc.typ, tc.v, v, tc.num, tc.spelled)
		}
		if o := v.observation("x"); o.Value == nil || *o.Value != tc.num {
			t.Errorf("observation of %v = %+v, want %v", tc.v, o, tc.num)
		}
	}
}
