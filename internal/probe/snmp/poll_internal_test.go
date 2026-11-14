package snmp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

const community = "s3cret-community-value"

var v2cSecret = map[string]string{"community": community}

func params(extra map[string]any) map[string]any {
	p := map[string]any{"secret": "core-switch"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func poll(fp *fakePoller, p map[string]any, secret map[string]string) ([]probe.Observation, Info) {
	return fp.Poll(context.Background(), probetest.LoopbackGuard(), p, "127.0.0.1", secret)
}

func TestPollReadsEveryValueType(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(123456))
	dev.set(".1.3.6.1.9.1", gosnmp.Integer, -42)
	dev.set(".1.3.6.1.9.2", gosnmp.Counter32, uint(4000000000))
	dev.set(".1.3.6.1.9.3", gosnmp.Counter64, uint64(1<<40))
	dev.set(".1.3.6.1.9.4", gosnmp.Gauge32, uint(77))
	dev.set(".1.3.6.1.9.5", gosnmp.TimeTicks, uint32(500))
	dev.set(".1.3.6.1.9.6", gosnmp.Uinteger32, uint32(9))
	dev.set(".1.3.6.1.9.7", gosnmp.OctetString, []byte("\x00UPS\x07 model\xff\r\n"))
	dev.set(".1.3.6.1.9.8", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.9")
	dev.set(".1.3.6.1.9.9", gosnmp.IPAddress, "10.0.0.1")
	dev.set(".1.3.6.1.9.10", gosnmp.OpaqueFloat, float32(1.5))
	dev.set(".1.3.6.1.9.11", gosnmp.Null, nil)
	dev.values[".1.3.6.1.9.12"] = gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance}
	dev.values[".1.3.6.1.9.13"] = gosnmp.SnmpPDU{Type: gosnmp.EndOfMibView}
	dev.set(".1.3.6.1.9.14", gosnmp.OctetString, []byte(strings.Repeat("ü", 300)))
	fp := newFakePoller(dev)

	oids := []any{
		"int=1.3.6.1.9.1", "c32=1.3.6.1.9.2", "c64=1.3.6.1.9.3", "gauge=1.3.6.1.9.4",
		"ticks=1.3.6.1.9.5", "uint=1.3.6.1.9.6", "str=1.3.6.1.9.7", "oid=1.3.6.1.9.8",
		"ip=.1.3.6.1.9.9", "float=1.3.6.1.9.10", "null=1.3.6.1.9.11", "inst=1.3.6.1.9.12",
		"end=1.3.6.1.9.13", "missing=1.3.6.1.9.99", "long=1.3.6.1.9.14",
	}
	obs, info := poll(fp, params(map[string]any{"oids": oids, "port": float64(1161), "timeout_seconds": float64(3)}), v2cSecret)
	m := byKey(t, obs)
	if info.InterfacesSkipped {
		t.Fatal("no interfaces asked for, none skipped")
	}

	wantValue(t, m, "snmp.up", "", 1)
	if o := m[key{"snmp.response_ms", ""}]; o.Value == nil || *o.Value < 0 {
		t.Fatalf("snmp.response_ms: %+v", o)
	}
	wantValue(t, m, "snmp.uptime_seconds", "", 1234.56)
	wantValue(t, m, "snmp.value", "int", -42)
	wantValue(t, m, "snmp.value", "c32", 4000000000)
	wantValue(t, m, "snmp.value", "c64", 1<<40)
	wantValue(t, m, "snmp.value", "gauge", 77)
	wantValue(t, m, "snmp.value", "ticks", 500)
	wantValue(t, m, "snmp.value", "uint", 9)
	wantValue(t, m, "snmp.value", "float", 1.5)
	wantLabel(t, m, "snmp.text", "str", "UPS model�")
	wantLabel(t, m, "snmp.text", "oid", ".1.3.6.1.4.1.9")
	wantLabel(t, m, "snmp.text", "ip", "10.0.0.1")
	if o := m[key{"snmp.text", "long"}]; o.Label == nil || len(*o.Label) > maxLabelLen {
		t.Fatalf("long text: %+v", o)
	}
	wantErr(t, m, "snmp.value", "null", errUnsupportedType)
	for _, item := range []string{"inst", "end", "missing"} {
		wantErr(t, m, "snmp.value", item, errNoSuchObject)
	}
	if len(m) != 3+len(oids) {
		t.Fatalf("want 3 host metrics and one per oid, got %d: %+v", len(m), obs)
	}

	if len(fp.dialed) != 1 {
		t.Fatalf("dialled %d times", len(fp.dialed))
	}
	d := fp.dialed[0]
	if d.target != netip.MustParseAddrPort("127.0.0.1:1161") || d.timeout != 3*time.Second || d.creds.community != community {
		t.Fatalf("dialled %v, timeout %v", d.target, d.timeout)
	}
	if !dev.closed {
		t.Fatal("client not closed")
	}
}

func TestPollBatchesSixteenOIDsPerRequest(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	oids := make([]any, 32)
	for i := range oids {
		oid := fmt.Sprintf(".1.3.6.1.9.%d", i+1)
		dev.set(oid, gosnmp.Integer, i)
		oids[i] = fmt.Sprintf("o%d=%s", i, oid)
	}
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, params(map[string]any{"oids": oids}), v2cSecret)))
	if len(dev.gets) != 3 || len(dev.gets[0]) != 16 || len(dev.gets[1]) != 16 || len(dev.gets[2]) != 1 {
		t.Fatalf("requests: %v", dev.gets)
	}
	if dev.gets[0][0] != oidSysUpTime {
		t.Fatalf("the first request does not start with sysUpTime: %v", dev.gets[0])
	}
	for i := range oids {
		wantValue(t, m, "snmp.value", fmt.Sprintf("o%d", i), float64(i))
	}
}

func first(obs []probe.Observation, _ Info) []probe.Observation { return obs }

func TestPollFirstRequestUnansweredIsDown(t *testing.T) {
	dev := newFakeDevice()
	dev.getErr = errFakeTimeout
	fp := newFakePoller(dev)
	obs, _ := poll(fp, params(map[string]any{"oids": []any{"x=1.3.6.1.9.1"}, "interfaces": true}), v2cSecret)
	probetest.WantOnly(t, obs, "snmp.up", 0)
	if len(dev.walked) != 0 {
		t.Fatalf("walked after no answer: %v", dev.walked)
	}
}

// TestPollRefusedCredentialsAreAnError: a v3 device that answers with a
// report refusing the user, digest or security level answered, but the
// poll could not be made; it is an error naming the secret, not up = 0.
func TestPollRefusedCredentialsAreAnError(t *testing.T) {
	v3 := map[string]string{"username": "u", "auth_protocol": "SHA", "auth_password": "auth-pass-value"}
	for _, err := range []error{gosnmp.ErrUnknownUsername, gosnmp.ErrWrongDigest, gosnmp.ErrUnknownSecurityLevel, gosnmp.ErrDecryption, fmt.Errorf("wrapped: %w", gosnmp.ErrWrongDigest)} {
		dev := newFakeDevice()
		dev.getErr = err
		fp := newFakePoller(dev)
		obs, _ := poll(fp, params(map[string]any{"version": "v3", "oids": []any{"x=1.3.6.1.9.1"}}), v3)
		m := byKey(t, obs)
		want := `the device did not accept the credentials of secret "core-switch"`
		for _, k := range []key{{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""}, {"snmp.value", "x"}} {
			wantErr(t, m, k.metric, k.item, want)
		}
		if len(m) != 4 {
			t.Fatalf("%v: %+v", err, obs)
		}
	}
}

// TestPollV3AnswerNotAcceptedIsRefusedCredentials: a v3 device that
// answered a request that still failed refused the credentials (gosnmp
// discards its unauthenticated report); a v2c device in the same case, or
// a v3 device that did not answer, is down.
func TestPollV3AnswerNotAcceptedIsRefusedCredentials(t *testing.T) {
	v3 := map[string]string{"username": "u", "auth_protocol": "SHA", "auth_password": "auth-pass-value"}
	for _, tc := range []struct {
		name     string
		version  string
		secret   map[string]string
		answered bool
		down     bool
	}{
		{"v3 answered", "v3", v3, true, false},
		{"v3 silent", "v3", v3, false, true},
		{"v2c answered", "v2c", v2cSecret, true, true},
	} {
		dev := newFakeDevice()
		dev.getErr, dev.replied = errors.New("incoming packet is not authentic, discarding"), tc.answered
		obs, _ := poll(newFakePoller(dev), params(map[string]any{"version": tc.version, "interfaces": true}), tc.secret)
		if tc.down {
			probetest.WantOnly(t, obs, "snmp.up", 0)
			continue
		}
		probetest.WantErrors(t, obs, `the device did not accept the credentials of secret "core-switch"`, hostMetrics...)
	}
}

func TestPollLaterRequestUnansweredIsAnErrorOnItsItems(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	oids := make([]any, 20)
	for i := range oids {
		oid := fmt.Sprintf(".1.3.6.1.9.%d", i+1)
		dev.set(oid, gosnmp.Integer, i)
		oids[i] = fmt.Sprintf("o%d=%s", i, oid)
	}
	dev.getErr, dev.getOK = errFakeTimeout, 1
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, params(map[string]any{"oids": oids}), v2cSecret)))
	wantValue(t, m, "snmp.up", "", 1)
	for i := range oids {
		if i < 15 {
			wantValue(t, m, "snmp.value", fmt.Sprintf("o%d", i), float64(i))
		} else {
			wantErr(t, m, "snmp.value", fmt.Sprintf("o%d", i), errNoAnswer)
		}
	}
}

func TestPollErrorStatusIsAnErrorOnItsItems(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	dev.set(".1.3.6.1.9.1", gosnmp.Integer, 5)
	dev.pduError = gosnmp.GenErr
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, params(map[string]any{"oids": []any{"x=1.3.6.1.9.1"}}), v2cSecret)))
	wantValue(t, m, "snmp.up", "", 1)
	want := "the device answered with error GenErr"
	wantErr(t, m, "snmp.uptime_seconds", "", want)
	wantErr(t, m, "snmp.value", "x", want)
}

func TestPollWithoutSysUpTimeReportsNoUptime(t *testing.T) {
	dev := newFakeDevice()
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, params(map[string]any{"interfaces": true}), v2cSecret)))
	wantValue(t, m, "snmp.up", "", 1)
	wantErr(t, m, "snmp.uptime_seconds", "", errNoSuchObject)
}

func TestPollBadSecretFailsEveryMetricWithoutDialling(t *testing.T) {
	dev := newFakeDevice()
	fp := newFakePoller(dev)
	obs, _ := poll(fp, params(map[string]any{"oids": []any{"x=1.3.6.1.9.1", "y=1.3.6.1.9.2"}}), map[string]string{"username": "nobody"})
	m := byKey(t, obs)
	want := `secret "core-switch": field "community" is missing`
	for _, k := range []key{{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""}, {"snmp.value", "x"}, {"snmp.value", "y"}} {
		wantErr(t, m, k.metric, k.item, want)
	}
	if len(m) != 5 || len(fp.dialed) != 0 {
		t.Fatalf("observations %+v, dialled %d", obs, len(fp.dialed))
	}
}

func TestPollBadParamsFailTheHostMetrics(t *testing.T) {
	fp := newFakePoller(newFakeDevice())
	obs, _ := poll(fp, map[string]any{}, v2cSecret)
	probetest.WantErrors(t, obs, errNoSecretParam, "snmp.up", "snmp.response_ms", "snmp.uptime_seconds")
	if len(fp.dialed) != 0 {
		t.Fatal("dialled with bad params")
	}
}

// TestPollGuardRefusalIsAnError: a target the guard refuses is a
// collection error on every metric, never up = 0, whether the address is
// refused up front or the dial is.
func TestPollGuardRefusalIsAnError(t *testing.T) {
	fp := newFakePoller(newFakeDevice())
	p := params(map[string]any{"oids": []any{"x=1.3.6.1.9.1"}, "interfaces": true})
	obs, _ := fp.Poll(context.Background(), probe.NewGuard(nil, nil), p, "127.0.0.1", v2cSecret)
	m := byKey(t, obs)
	for _, k := range []key{{"snmp.up", ""}, {"snmp.response_ms", ""}, {"snmp.uptime_seconds", ""}, {"snmp.value", "x"}} {
		wantErr(t, m, k.metric, k.item, probe.ErrNotAllowed.Error())
	}
	if len(fp.dialed) != 0 {
		t.Fatal("dialled a refused address")
	}

	fp.dialErr = fmt.Errorf("error establishing connection to host: %w", probe.ErrNotAllowed)
	obs, _ = poll(fp, p, v2cSecret)
	wantErr(t, byKey(t, obs), "snmp.up", "", probe.ErrNotAllowed.Error())

	fp.dialErr = errors.New("network is unreachable")
	obs, _ = poll(fp, p, v2cSecret)
	probetest.WantOnly(t, obs, "snmp.up", 0)
}

func TestPollBadAddressFailsEveryMetric(t *testing.T) {
	fp := newFakePoller(newFakeDevice())
	obs, _ := fp.Poll(context.Background(), probetest.LoopbackGuard(), params(map[string]any{"interfaces": true}), "", v2cSecret)
	wantErr(t, byKey(t, obs), "snmp.up", "", "host has no address")
}

// TestPollNeverQuotesTheSecret: no outcome carries a value of the secret,
// even when the library's error text does.
func TestPollNeverQuotesTheSecret(t *testing.T) {
	leaky := fmt.Errorf("bad packet Community:%s", community)
	for _, tc := range []struct {
		name string
		dev  func(*fakeDevice)
		fp   func(*fakePoller)
	}{
		{"first get fails", func(d *fakeDevice) { d.getErr = leaky }, nil},
		{"later get fails", func(d *fakeDevice) {
			d.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
			d.getErr, d.getOK = leaky, 1
		}, nil},
		{"walk fails", func(d *fakeDevice) {
			d.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
			d.walkErr[colIfDescr] = leaky
		}, nil},
		{"dial fails", nil, func(fp *fakePoller) { fp.dialErr = leaky }},
	} {
		dev := newFakeDevice()
		if tc.dev != nil {
			tc.dev(dev)
		}
		fp := newFakePoller(dev)
		if tc.fp != nil {
			tc.fp(fp)
		}
		oids := make([]any, 20)
		for i := range oids {
			oids[i] = fmt.Sprintf("o%d=1.3.6.1.9.%d", i, i+1)
		}
		obs, _ := poll(fp, params(map[string]any{"oids": oids, "interfaces": true}), v2cSecret)
		for _, o := range obs {
			if strings.Contains(o.Err, community) || (o.Label != nil && strings.Contains(*o.Label, community)) {
				t.Fatalf("%s: %+v quotes the community", tc.name, o)
			}
		}
	}
}

// TestPollBadOIDFailsOnlyItsItem: an entry whose OID SNMP cannot encode is
// an error on its own item; it is not asked for, and neither the device's
// state nor its siblings suffer.
func TestPollBadOIDFailsOnlyItsItem(t *testing.T) {
	dev := newFakeDevice()
	dev.set(oidSysUpTime, gosnmp.TimeTicks, uint32(1))
	dev.set(".1.3.6.1.9.1", gosnmp.Integer, 7)
	fp := newFakePoller(dev)
	m := byKey(t, first(poll(fp, params(map[string]any{"oids": []any{"bad=3.1", "ok=1.3.6.1.9.1", "worse=1.40.1"}}), v2cSecret)))
	wantValue(t, m, "snmp.up", "", 1)
	wantValue(t, m, "snmp.value", "ok", 7)
	wantErr(t, m, "snmp.value", "bad", errBadOID)
	wantErr(t, m, "snmp.value", "worse", errBadOID)
	if len(dev.gets) != 1 || len(dev.gets[0]) != 2 {
		t.Fatalf("requests %v, want sysUpTime and ok only", dev.gets)
	}
}

// TestPollResponseTimeIsTheClientsRoundTrip: snmp.response_ms is the first
// request's own round trip as the client measured it (without a v3 engine
// discovery before it).
func TestPollResponseTimeIsTheClientsRoundTrip(t *testing.T) {
	dev := newFakeDevice()
	dev.rtt = 7 * time.Millisecond
	m := byKey(t, first(poll(newFakePoller(dev), params(map[string]any{"interfaces": true}), v2cSecret)))
	wantValue(t, m, "snmp.response_ms", "", 7)
}
