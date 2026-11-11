package checks_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checks"
)

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
