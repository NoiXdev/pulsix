package checks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

// The accept/refuse tables below predate internal/checkplugins (backlog
// 3.54) and still run through ValidateDefinitionShape, the public path
// every definition write takes: they are what keeps the descriptors'
// declarative rules from silently weakening what the hand-written
// validators of each plugin used to refuse.

// TestSyntheticParamsAccepted: every parameter set the spec's Plugins table
// allows passes, starting with each plugin's defaults spelled out (the
// numbers as a JSON body decodes them, float64) and an empty set wherever
// no parameter is required.
func TestSyntheticParamsAccepted(t *testing.T) {
	cases := []struct {
		name   string
		plugin string
		params map[string]any
	}{
		{"icmp defaults", checks.PluginICMP, map[string]any{"count": 3.0, "timeout_s": 5.0}},
		{"icmp no params", checks.PluginICMP, nil},
		{"icmp bounds", checks.PluginICMP, map[string]any{"count": 10.0, "timeout_s": 30.0}},
		{"icmp Go ints", checks.PluginICMP, map[string]any{"count": 1, "timeout_s": int64(1)}},
		{"tcp defaults", checks.PluginTCP, map[string]any{"port": 22.0, "timeout_s": 5.0}},
		{"tcp banner", checks.PluginTCP, map[string]any{"port": 25.0, "send": "EHLO x\r\n", "expect": "250", "timeout_s": 30.0}},
		{"tcp empty send", checks.PluginTCP, map[string]any{"port": 65535.0, "send": ""}},
		{"http defaults", checks.PluginHTTP, map[string]any{
			"url": "https://{address}/", "method": "GET", "follow_redirects": true, "verify_tls": true, "timeout_s": 10.0,
		}},
		{"http url only", checks.PluginHTTP, map[string]any{"url": "http://example.com:8080/health?x=1"}},
		{"http head no redirects", checks.PluginHTTP, map[string]any{
			"url": "http://{address}:8080/", "method": "HEAD", "follow_redirects": false, "verify_tls": false,
			"body_contains": "ok", "timeout_s": 60.0,
		}},
		{"tls defaults", checks.PluginTLS, map[string]any{"port": 443.0, "timeout_s": 10.0}},
		{"tls no params", checks.PluginTLS, nil},
		{"tls server name", checks.PluginTLS, map[string]any{"port": 8443.0, "server_name": "example.com"}},
		{"dns defaults", checks.PluginDNS, map[string]any{"name": "{address}", "type": "A", "timeout_s": 5.0}},
		{"dns no params", checks.PluginDNS, nil},
		{"dns every type", checks.PluginDNS, map[string]any{"type": "AAAA"}},
		{"dns cname", checks.PluginDNS, map[string]any{"type": "CNAME"}},
		{"dns mx", checks.PluginDNS, map[string]any{"type": "MX"}},
		{"dns txt", checks.PluginDNS, map[string]any{"type": "TXT"}},
		{"dns ns", checks.PluginDNS, map[string]any{"type": "NS", "name": "example.com", "expect": "ns1.example.com."}},
		{"dns server ipv4", checks.PluginDNS, map[string]any{"server": "192.0.2.53"}},
		{"dns server ipv4 port", checks.PluginDNS, map[string]any{"server": "192.0.2.53:5353"}},
		{"dns server ipv6", checks.PluginDNS, map[string]any{"server": "2001:db8::53"}},
		{"dns server ipv6 port", checks.PluginDNS, map[string]any{"server": "[2001:db8::53]:53"}},
		{"dns server ipv6 bracketed", checks.PluginDNS, map[string]any{"server": "[2001:db8::53]"}},
		{"dns name trailing dot", checks.PluginDNS, map[string]any{"name": "example.com."}},
		{"dns name srv style", checks.PluginDNS, map[string]any{"name": "_sip._tcp.example.com"}},
		{"dns name with placeholder", checks.PluginDNS, map[string]any{"name": "www.{address}"}},
		{"dns name single label", checks.PluginDNS, map[string]any{"name": "intranet"}},
		{"dns name 253 bytes", checks.PluginDNS, map[string]any{"name": dnsName(253)}},
		{"dns name 63-byte label", checks.PluginDNS, map[string]any{"name": strings.Repeat("a", 63) + ".example"}},
		{"long string at the cap", checks.PluginTCP, map[string]any{"port": 1.0, "expect": strings.Repeat("x", 500)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := checks.ValidateDefinitionShape(tc.plugin, "", tc.params, time.Minute); err != nil {
				t.Fatalf("ValidateDefinitionShape(%s, %v) = %v, want nil", tc.plugin, tc.params, err)
			}
		})
	}
}

// TestSyntheticParamsRefused: every rule of the spec's Validation section,
// one case per way to break it. Unknown keys are refused on every plugin.
func TestSyntheticParamsRefused(t *testing.T) {
	cases := []struct {
		name   string
		plugin string
		params map[string]any
	}{
		// icmp
		{"icmp count 11", checks.PluginICMP, map[string]any{"count": 11.0}},
		{"icmp count 0", checks.PluginICMP, map[string]any{"count": 0.0}},
		{"icmp count fractional", checks.PluginICMP, map[string]any{"count": 2.5}},
		{"icmp count as string", checks.PluginICMP, map[string]any{"count": "3"}},
		{"icmp timeout 31", checks.PluginICMP, map[string]any{"timeout_s": 31.0}},
		{"icmp unknown key", checks.PluginICMP, map[string]any{"cnt": 3.0}},
		// tcp
		{"tcp without port", checks.PluginTCP, map[string]any{"timeout_s": 5.0}},
		{"tcp no params", checks.PluginTCP, nil},
		{"tcp port 70000", checks.PluginTCP, map[string]any{"port": 70000.0}},
		{"tcp port 0", checks.PluginTCP, map[string]any{"port": 0.0}},
		{"tcp port bool", checks.PluginTCP, map[string]any{"port": true}},
		{"tcp empty expect", checks.PluginTCP, map[string]any{"port": 22.0, "expect": ""}},
		{"tcp send not a string", checks.PluginTCP, map[string]any{"port": 22.0, "send": 1.0}},
		{"tcp timeout 31", checks.PluginTCP, map[string]any{"port": 22.0, "timeout_s": 31.0}},
		{"tcp string over the cap", checks.PluginTCP, map[string]any{"port": 22.0, "expect": strings.Repeat("x", 501)}},
		{"tcp unknown key", checks.PluginTCP, map[string]any{"port": 22.0, "banner": "x"}},
		// http
		{"http without url", checks.PluginHTTP, map[string]any{"method": "GET"}},
		{"http ftp url", checks.PluginHTTP, map[string]any{"url": "ftp://x"}},
		{"http no scheme", checks.PluginHTTP, map[string]any{"url": "example.com/x"}},
		{"http no host", checks.PluginHTTP, map[string]any{"url": "http:///path"}},
		{"http unparsable", checks.PluginHTTP, map[string]any{"url": "http://[::1"}},
		{"http placeholder in port", checks.PluginHTTP, map[string]any{"url": "http://example.com:{address}/"}},
		{"http userinfo", checks.PluginHTTP, map[string]any{"url": "https://user:pass@example.com/"}},
		{"http user only", checks.PluginHTTP, map[string]any{"url": "https://user@{address}/"}},
		{"http method POST", checks.PluginHTTP, map[string]any{"url": "http://x", "method": "POST"}},
		{"http method lower case", checks.PluginHTTP, map[string]any{"url": "http://x", "method": "get"}},
		{"http follow_redirects not bool", checks.PluginHTTP, map[string]any{"url": "http://x", "follow_redirects": "yes"}},
		{"http verify_tls not bool", checks.PluginHTTP, map[string]any{"url": "http://x", "verify_tls": 1.0}},
		{"http empty body_contains", checks.PluginHTTP, map[string]any{"url": "http://x", "body_contains": ""}},
		{"http timeout 61", checks.PluginHTTP, map[string]any{"url": "http://x", "timeout_s": 61.0}},
		{"http url over the cap", checks.PluginHTTP, map[string]any{"url": "http://x/" + strings.Repeat("a", 500)}},
		{"http unknown key", checks.PluginHTTP, map[string]any{"url": "http://x", "headers": map[string]any{}}},
		// tls
		{"tls port 70000", checks.PluginTLS, map[string]any{"port": 70000.0}},
		{"tls empty server_name", checks.PluginTLS, map[string]any{"server_name": ""}},
		{"tls blank server_name", checks.PluginTLS, map[string]any{"server_name": "  "}},
		{"tls timeout 31", checks.PluginTLS, map[string]any{"timeout_s": 31.0}},
		{"tls unknown key", checks.PluginTLS, map[string]any{"sni": "x"}},
		// dns
		{"dns type SRV", checks.PluginDNS, map[string]any{"type": "SRV"}},
		{"dns type lower case", checks.PluginDNS, map[string]any{"type": "a"}},
		{"dns server not an ip", checks.PluginDNS, map[string]any{"server": "not-an-ip"}},
		{"dns server hostname port", checks.PluginDNS, map[string]any{"server": "dns.example:53"}},
		{"dns server port 0", checks.PluginDNS, map[string]any{"server": "192.0.2.53:0"}},
		{"dns server port 70000", checks.PluginDNS, map[string]any{"server": "192.0.2.53:70000"}},
		{"dns server garbage after an ipv6 address", checks.PluginDNS, map[string]any{"server": "2001:db8::53:53:x"}},
		{"dns server unclosed bracket", checks.PluginDNS, map[string]any{"server": "[2001:db8::53"}},
		{"dns server bracketed ipv4", checks.PluginDNS, map[string]any{"server": "[192.0.2.53]"}},
		{"dns empty name", checks.PluginDNS, map[string]any{"name": ""}},
		{"dns name with a space", checks.PluginDNS, map[string]any{"name": "exa mple.com"}},
		{"dns name with an at sign", checks.PluginDNS, map[string]any{"name": "ex@mple.com"}},
		{"dns name empty label", checks.PluginDNS, map[string]any{"name": "example..com"}},
		{"dns name leading dot", checks.PluginDNS, map[string]any{"name": ".example.com"}},
		{"dns name root only", checks.PluginDNS, map[string]any{"name": "."}},
		{"dns name two trailing dots", checks.PluginDNS, map[string]any{"name": "example.com.."}},
		{"dns name placeholder with a space", checks.PluginDNS, map[string]any{"name": "{address} x"}},
		{"dns name 254 bytes", checks.PluginDNS, map[string]any{"name": dnsName(254)}},
		{"dns name 64-byte label", checks.PluginDNS, map[string]any{"name": strings.Repeat("a", 64) + ".example"}},
		{"dns name not a string", checks.PluginDNS, map[string]any{"name": 5.0}},
		{"dns empty expect", checks.PluginDNS, map[string]any{"expect": ""}},
		{"dns timeout 31", checks.PluginDNS, map[string]any{"timeout_s": 31.0}},
		{"dns unknown key", checks.PluginDNS, map[string]any{"qtype": "A"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checks.ValidateDefinitionShape(tc.plugin, "", tc.params, time.Minute)
			if !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("ValidateDefinitionShape(%s, %v) = %v, want ErrInvalidInput", tc.plugin, tc.params, err)
			}
		})
	}
}

// dnsName is a valid DNS name of exactly n bytes: 49-byte labels.
func dnsName(n int) string {
	var b strings.Builder
	for b.Len() < n {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strings.Repeat("a", min(49, n-b.Len())))
	}
	return b.String()
}

// TestValidateDockerParams pins the docker plugin's parameter shape
// (backlog 3.12): selection, label and socket, nothing else. The opt-out
// semantics of label under selection "all" belong to the agent; the manager
// only validates shape, so a label next to "all" is accepted.
func TestValidateDockerParams(t *testing.T) {
	accepted := []struct {
		name   string
		params map[string]any
	}{
		{"empty", map[string]any{}},
		{"nil", nil},
		{"all", map[string]any{"selection": "all"}},
		{"labeled", map[string]any{"selection": "labeled"}},
		{"labeled with key only", map[string]any{"selection": "labeled", "label": "pulsix.monitor"}},
		{"slash in key", map[string]any{"label": "com.example/watch=yes"}},
		{"all with a label (opt-out key)", map[string]any{"selection": "all", "label": "pulsix.monitor=true"}},
		{"empty value after =", map[string]any{"label": "k="}},
		{"max key", map[string]any{"label": strings.Repeat("k", 255)}},
		{"max value", map[string]any{"label": "k=" + strings.Repeat("v", 255)}},
		{"unix socket", map[string]any{"socket": "/var/run/docker.sock"}},
		{"podman socket", map[string]any{"socket": "/run/podman/podman.sock"}},
		{"named pipe", map[string]any{"socket": "npipe:////./pipe/docker_engine"}},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			if _, err := checks.ValidateDefinitionShape("docker", "docker", tc.params, time.Minute); err != nil {
				t.Fatalf("ValidateDefinitionShape(docker, %v) = %v, want nil", tc.params, err)
			}
		})
	}
	refused := []struct {
		name   string
		params map[string]any
	}{
		{"unknown selection", map[string]any{"selection": "some"}},
		{"selection not a string", map[string]any{"selection": 1}},
		{"empty label", map[string]any{"label": ""}},
		{"label not a string", map[string]any{"label": 1}},
		{"empty key", map[string]any{"label": "=x"}},
		{"space in key", map[string]any{"label": "bad key=x"}},
		{"long value", map[string]any{"label": "k=" + strings.Repeat("v", 256)}},
		{"control in value", map[string]any{"label": "k=a\nb"}},
		{"long key", map[string]any{"label": strings.Repeat("k", 256)}},
		{"empty socket", map[string]any{"socket": ""}},
		{"socket not a string", map[string]any{"socket": 1}},
		{"relative socket", map[string]any{"socket": "docker.sock"}},
		{"tcp socket", map[string]any{"socket": "tcp://10.0.0.1:2375"}},
		{"pipe without name", map[string]any{"socket": "npipe:////./pipe/"}},
		{"nul in socket", map[string]any{"socket": "/a\x00b"}},
		{"long socket", map[string]any{"socket": "/" + strings.Repeat("s", 255)}},
		{"unknown parameter", map[string]any{"units": []any{"x"}}},
	}
	for _, tc := range refused {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			_, err := checks.ValidateDefinitionShape("docker", "docker", tc.params, time.Minute)
			if !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("ValidateDefinitionShape(docker, %v) = %v, want ErrInvalidInput", tc.params, err)
			}
		})
	}
}

func anyList(n int, entry string) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = entry
	}
	return out
}

// TestValidateHardwareParams pins the temperature, smart and raid parameter
// shapes (backlog 3.9): one optional list of strings each, nothing else.
func TestValidateHardwareParams(t *testing.T) {
	type tc struct {
		name   string
		plugin string
		params map[string]any
		ok     bool
	}
	cases := []tc{
		{"temperature empty", "temperature", map[string]any{}, true},
		{"temperature nil", "temperature", nil, true},
		{"temperature sensors", "temperature", map[string]any{"sensors": []any{"coretemp_core_0", "nvme_composite"}}, true},
		{"temperature []string", "temperature", map[string]any{"sensors": []string{"a"}}, true},
		{"temperature 64 entries", "temperature", map[string]any{"sensors": anyList(64, "s")}, true},
		{"temperature 65 entries", "temperature", map[string]any{"sensors": anyList(65, "s")}, false},
		{"temperature max entry", "temperature", map[string]any{"sensors": []any{strings.Repeat("s", 255)}}, true},
		{"temperature long entry", "temperature", map[string]any{"sensors": []any{strings.Repeat("s", 256)}}, false},
		{"temperature empty entry", "temperature", map[string]any{"sensors": []any{""}}, false},
		{"temperature control char", "temperature", map[string]any{"sensors": []any{"a\nb"}}, false},
		{"temperature not a list", "temperature", map[string]any{"sensors": "a"}, false},
		{"temperature non-string entry", "temperature", map[string]any{"sensors": []any{1}}, false},
		{"temperature unknown param", "temperature", map[string]any{"devices": []any{"/dev/sda"}}, false},

		{"smart empty", "smart", map[string]any{}, true},
		{"smart sata", "smart", map[string]any{"devices": []any{"/dev/sda", "/dev/sdb"}}, true},
		{"smart nvme", "smart", map[string]any{"devices": []any{"/dev/nvme0", "/dev/nvme0n1"}}, true},
		{"smart by-id", "smart", map[string]any{"devices": []any{"/dev/disk/by-id/ata-WDC_WD10-1.0:x+y@z"}}, true},
		{"smart megaraid comma", "smart", map[string]any{"devices": []any{"/dev/csmi0,1"}}, true},
		{"smart windows pd", "smart", map[string]any{"devices": []any{"/dev/pd0"}}, true},
		{"smart 64 entries", "smart", map[string]any{"devices": anyList(64, "/dev/sda")}, true},
		{"smart 65 entries", "smart", map[string]any{"devices": anyList(65, "/dev/sda")}, false},
		{"smart relative", "smart", map[string]any{"devices": []any{"sda"}}, false},
		{"smart windows backslash path", "smart", map[string]any{"devices": []any{`\\.\PhysicalDrive0`}}, false},
		{"smart option-like", "smart", map[string]any{"devices": []any{"-a"}}, false},
		{"smart space", "smart", map[string]any{"devices": []any{"/dev/my disk"}}, false},
		{"smart shell metachar", "smart", map[string]any{"devices": []any{"/dev/sda;rm"}}, false},
		{"smart control char", "smart", map[string]any{"devices": []any{"/dev/sda\n"}}, false},
		{"smart empty entry", "smart", map[string]any{"devices": []any{""}}, false},
		{"smart long entry", "smart", map[string]any{"devices": []any{"/dev/" + strings.Repeat("s", 251)}}, false},
		{"smart max entry", "smart", map[string]any{"devices": []any{"/dev/" + strings.Repeat("s", 250)}}, true},
		{"smart bare /dev/", "smart", map[string]any{"devices": []any{"/dev/"}}, false},
		{"smart parent segment", "smart", map[string]any{"devices": []any{"/dev/../etc/shadow"}}, false},
		{"smart parent segment short", "smart", map[string]any{"devices": []any{"/dev/../x"}}, false},
		{"smart trailing parent segment", "smart", map[string]any{"devices": []any{"/dev/disk/.."}}, false},
		{"smart inner parent segment", "smart", map[string]any{"devices": []any{"/dev/disk/../sda"}}, false},
		{"smart dots inside a name", "smart", map[string]any{"devices": []any{"/dev/disk/by-id/nvme-eui.00..25"}}, true},
		{"smart current segment", "smart", map[string]any{"devices": []any{"/dev/./sda"}}, true},
		{"temperature multibyte over 255 bytes", "temperature", map[string]any{"sensors": []any{strings.Repeat("ä", 128)}}, false},
		{"smart multibyte over 255 bytes", "smart", map[string]any{"devices": []any{"/dev/" + strings.Repeat("ä", 126)}}, false},
		{"smart not a list", "smart", map[string]any{"devices": "/dev/sda"}, false},
		{"smart unknown param", "smart", map[string]any{"arrays": []any{"md0"}}, false},

		{"raid empty", "raid", map[string]any{}, true},
		{"raid arrays", "raid", map[string]any{"arrays": []any{"md0", "md127"}}, true},
		{"raid 65 entries", "raid", map[string]any{"arrays": anyList(65, "md0")}, false},
		{"raid empty entry", "raid", map[string]any{"arrays": []any{""}}, false},
		{"raid long entry", "raid", map[string]any{"arrays": []any{strings.Repeat("a", 256)}}, false},
		{"raid control char", "raid", map[string]any{"arrays": []any{"md\x000"}}, false},
		{"raid not a list", "raid", map[string]any{"arrays": 3}, false},
		{"raid unknown param", "raid", map[string]any{"sensors": []any{"x"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := checks.ValidateDefinitionShape(c.plugin, c.plugin, c.params, time.Minute)
			if c.ok && err != nil {
				t.Fatalf("ValidateDefinitionShape(%s, %v) = %v, want nil", c.plugin, c.params, err)
			}
			if !c.ok && !errors.Is(err, checks.ErrInvalidInput) {
				t.Fatalf("ValidateDefinitionShape(%s, %v) = %v, want ErrInvalidInput", c.plugin, c.params, err)
			}
		})
	}
}
