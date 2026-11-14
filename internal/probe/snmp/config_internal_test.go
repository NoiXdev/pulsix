package snmp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestBoundsMatchTheDescriptor: the probe's defaults and bounds are the
// ones internal/checkplugins validates params with.
func TestBoundsMatchTheDescriptor(t *testing.T) {
	d, ok := checkplugins.Get("snmp")
	if !ok {
		t.Fatal("no snmp descriptor")
	}
	specs := map[string]checkplugins.ParamSpec{}
	for _, p := range d.Params {
		specs[p.Name] = p
	}
	if p := specs["port"]; p.Default != int64(defPort) {
		t.Errorf("port default %v, probe %d", p.Default, defPort)
	}
	if p := specs["timeout_seconds"]; p.Default != int64(defTimeoutSecs) || p.Min == nil || *p.Min != 1 || p.Max == nil || *p.Max != maxTimeoutSecs {
		t.Errorf("timeout_seconds %+v, probe 1..%d default %d", p, maxTimeoutSecs, defTimeoutSecs)
	}
	if p := specs["oids"]; p.MaxEntries != maxOIDs || p.Format != checkplugins.FormatSNMPOID {
		t.Errorf("oids %+v, probe at most %d", p, maxOIDs)
	}
	if p := specs["version"]; len(p.Options) != 2 || p.Options[0] != "v2c" || p.Options[1] != "v3" || p.Default != "v2c" {
		t.Errorf("version %+v", p)
	}
}

func TestParseSettingsDefaultsAndOIDs(t *testing.T) {
	s, err := parseSettings(map[string]any{
		"secret": "core",
		"oids":   []any{"load=1.3.6.1.4.1.2021.10.1.5.1", "name=.1.3.6.1.2.1.1.5.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.secret != "core" || s.version != "v2c" || s.port != 161 || s.timeout != 5*time.Second || s.interfaces {
		t.Fatalf("defaults: %+v", s)
	}
	want := []oidEntry{{name: "load", oid: ".1.3.6.1.4.1.2021.10.1.5.1"}, {name: "name", oid: ".1.3.6.1.2.1.1.5.0"}}
	if len(s.oids) != 2 || s.oids[0] != want[0] || s.oids[1] != want[1] {
		t.Fatalf("oids = %+v, want %+v", s.oids, want)
	}

	s, err = parseSettings(map[string]any{
		"secret": "core", "version": "v3", "port": float64(1161), "timeout_seconds": float64(2),
		"oids": []string{"x=1.3.6.1.2.1.1.3.0"}, "interfaces": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.version != "v3" || s.port != 1161 || s.timeout != 2*time.Second || !s.interfaces || len(s.oids) != 1 {
		t.Fatalf("explicit params: %+v", s)
	}
}

func TestParseSettingsRefusesWhatTheDescriptorRefuses(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"no secret":      {"interfaces": true},
		"bad version":    {"secret": "s", "version": "v1", "interfaces": true},
		"bad port":       {"secret": "s", "port": float64(70000), "interfaces": true},
		"bad timeout":    {"secret": "s", "timeout_seconds": float64(31), "interfaces": true},
		"zero timeout":   {"secret": "s", "timeout_seconds": float64(0), "interfaces": true},
		"entry no name":  {"secret": "s", "oids": []any{"=1.3.6"}},
		"entry no equal": {"secret": "s", "oids": []any{"x"}},
		"entry bad name": {"secret": "s", "oids": []any{"a b=1.3.6"}},
		"entry not str":  {"secret": "s", "oids": []any{float64(1)}},
		"too many oids":  {"secret": "s", "oids": manyOIDs(33)},
		"nothing to ask": {"secret": "s"},
	} {
		if _, err := parseSettings(params); err == nil {
			t.Errorf("%s: parseSettings(%v) accepted", name, params)
		}
	}
}

func manyOIDs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "o" + strings.Repeat("x", i) + "=1.3.6.1"
	}
	return out
}

func TestCredentialsV2C(t *testing.T) {
	c, err := credentialsFor("core", "v2c", map[string]string{"community": "s3cret-community"})
	if err != nil {
		t.Fatal(err)
	}
	if c.version != gosnmp.Version2c || c.community != "s3cret-community" || c.usm != nil {
		t.Fatalf("v2c credentials: version %v, usm %v", c.version, c.usm)
	}
}

func TestCredentialsV3(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret map[string]string
		flags  gosnmp.SnmpV3MsgFlags
		auth   gosnmp.SnmpV3AuthProtocol
		priv   gosnmp.SnmpV3PrivProtocol
	}{
		{"authNoPriv SHA", map[string]string{"username": "u", "auth_protocol": "SHA", "auth_password": "auth-pass-1"}, gosnmp.AuthNoPriv, gosnmp.SHA, gosnmp.NoPriv},
		{"authNoPriv SHA-256", map[string]string{"username": "u", "auth_protocol": "SHA-256", "auth_password": "auth-pass-1"}, gosnmp.AuthNoPriv, gosnmp.SHA256, gosnmp.NoPriv},
		{"authPriv SHA-512 AES", map[string]string{"username": "u", "auth_protocol": "SHA-512", "auth_password": "auth-pass-1", "priv_protocol": "AES", "priv_password": "priv-pass-1"}, gosnmp.AuthPriv, gosnmp.SHA512, gosnmp.AES},
		{"authPriv SHA AES-256", map[string]string{"username": "u", "auth_protocol": "SHA", "auth_password": "auth-pass-1", "priv_protocol": "AES-256", "priv_password": "priv-pass-1"}, gosnmp.AuthPriv, gosnmp.SHA, gosnmp.AES256},
	} {
		c, err := credentialsFor("core", "v3", tc.secret)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if c.version != gosnmp.Version3 || c.flags != tc.flags || c.usm == nil {
			t.Fatalf("%s: version %v flags %v", tc.name, c.version, c.flags)
		}
		if c.usm.UserName != "u" || c.usm.AuthenticationProtocol != tc.auth || c.usm.PrivacyProtocol != tc.priv ||
			c.usm.AuthenticationPassphrase != tc.secret["auth_password"] || c.usm.PrivacyPassphrase != tc.secret["priv_password"] {
			t.Fatalf("%s: usm %+v", tc.name, c.usm.SafeString())
		}
	}
}

// TestCredentialsErrorsNameTheFieldNeverAValue: every refusal names the
// secret and the field, and no value of the secret appears in the text.
func TestCredentialsErrorsNameTheFieldNeverAValue(t *testing.T) {
	const (
		authPass = "AUTHPASSWORDVALUE"
		privPass = "PRIVPASSWORDVALUE"
	)
	v3 := func(drop string, set map[string]string) map[string]string {
		m := map[string]string{"username": "USERNAMEVALUE", "auth_protocol": "SHA", "auth_password": authPass, "priv_protocol": "AES", "priv_password": privPass}
		delete(m, drop)
		for k, v := range set {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		name, version string
		secret        map[string]string
		field         string
	}{
		{"nil secret", "v2c", nil, "community"},
		{"no community", "v2c", map[string]string{"username": "x"}, "community"},
		{"empty community", "v2c", map[string]string{"community": ""}, "community"},
		{"no username", "v3", v3("username", nil), "username"},
		{"no auth protocol", "v3", v3("auth_protocol", nil), "auth_protocol"},
		{"bad auth protocol", "v3", v3("", map[string]string{"auth_protocol": "MD5VALUE"}), "auth_protocol"},
		{"no auth password: noAuthNoPriv", "v3", v3("auth_password", nil), "auth_password"},
		{"short auth password", "v3", v3("", map[string]string{"auth_password": "SHORT"}), "auth_password"},
		{"bad priv protocol", "v3", v3("", map[string]string{"priv_protocol": "DESVALUE"}), "priv_protocol"},
		{"priv protocol without password", "v3", v3("priv_password", nil), "priv_password"},
		{"priv password without protocol", "v3", v3("priv_protocol", nil), "priv_protocol"},
		{"short priv password", "v3", v3("", map[string]string{"priv_password": "TINY"}), "priv_password"},
	} {
		_, err := credentialsFor("core-switch", tc.version, tc.secret)
		if err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		msg := err.Error()
		if !strings.Contains(msg, `secret "core-switch"`) || !strings.Contains(msg, `field "`+tc.field+`"`) {
			t.Fatalf("%s: %q does not name the secret and field %q", tc.name, msg, tc.field)
		}
		for _, v := range tc.secret {
			if v != "" && strings.Contains(msg, v) {
				t.Fatalf("%s: %q quotes a value of the secret", tc.name, msg)
			}
		}
	}
}

func TestCredentialsNeverFormatTheirValues(t *testing.T) {
	c, err := credentialsFor("core", "v2c", map[string]string{"community": "s3cret-community"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{c.String(), c.GoString()} {
		if strings.Contains(s, "s3cret") {
			t.Fatalf("credentials format as %q", s)
		}
	}
}

// TestParseSettingsBadOIDFailsOnlyItsEntry: an entry with a good name but
// an OID the probe cannot ask for is kept, marked bad, so only its item
// fails.
func TestParseSettingsBadOIDFailsOnlyItsEntry(t *testing.T) {
	s, err := parseSettings(map[string]any{"secret": "s", "oids": []any{"ok=1.3.6.1", "a=3.1", "b=1.40.1", "c=2.4294967295", "d=1.3.a", "e="}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.oids) != 6 || s.oids[0].bad {
		t.Fatalf("oids %+v", s.oids)
	}
	for _, e := range s.oids[1:] {
		if !e.bad {
			t.Errorf("entry %q not marked bad", e.name)
		}
	}
}

// TestEncodableMatchesTheFormat: every OID the format accepts is one
// gosnmp's encoder takes, and the encoder refuses the first/second-arc
// cases the format refuses (X.690 8.19). The encoder is laxer on syntax
// (leading zeros, empty arcs), which the format refuses on its own.
func TestEncodableMatchesTheFormat(t *testing.T) {
	raw, err := os.ReadFile("../../checkplugins/testdata/formats.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Format, Input string
		Valid         bool
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, v := range vectors {
		name, oid, ok := strings.Cut(v.Input, "=")
		if v.Format != "snmpOID" || !ok || !checkplugins.FormatSecretName.Valid(name) || strings.Contains(oid, "=") {
			continue
		}
		if !v.Valid {
			continue
		}
		n++
		if !encodable(oid) {
			t.Errorf("encodable(%q) = false, the format accepts it", oid)
		}
	}
	if n < 10 {
		t.Fatalf("only %d vectors checked", n)
	}
	for _, oid := range []string{"3.1", "3.6.1.2", "4294967295.1", "0.40", "1.40.1", "1.4294967295", "2.4294967216", "2.4294967295", "1.4294967296", "1"} {
		if encodable(oid) {
			t.Errorf("encodable(%q) = true, the format refuses it", oid)
		}
	}
}
