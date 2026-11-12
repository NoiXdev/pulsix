package checkplugins_test

import (
	"strings"
	"testing"
)

// TestSyntheticParamsAccepted, ported from internal/checks: every parameter
// set the synthetic spec's Plugins table allows passes, starting with each
// plugin's defaults spelled out (the numbers as a JSON body decodes them,
// float64) and an empty set wherever no parameter is required.
func TestSyntheticParamsAccepted(t *testing.T) {
	runParamCases(t, []paramCase{
		{"icmp defaults", "icmp", map[string]any{"count": 3.0, "timeout_s": 5.0}, true},
		{"icmp no params", "icmp", nil, true},
		{"icmp bounds", "icmp", map[string]any{"count": 10.0, "timeout_s": 30.0}, true},
		{"icmp Go ints", "icmp", map[string]any{"count": 1, "timeout_s": int64(1)}, true},
		{"tcp defaults", "tcp", map[string]any{"port": 22.0, "timeout_s": 5.0}, true},
		{"tcp banner", "tcp", map[string]any{"port": 25.0, "send": "EHLO x\r\n", "expect": "250", "timeout_s": 30.0}, true},
		{"tcp empty send", "tcp", map[string]any{"port": 65535.0, "send": ""}, true},
		{"http defaults", "http", map[string]any{
			"url": "https://{address}/", "method": "GET", "follow_redirects": true, "verify_tls": true, "timeout_s": 10.0,
		}, true},
		{"http url only", "http", map[string]any{"url": "http://example.com:8080/health?x=1"}, true},
		{"http head no redirects", "http", map[string]any{
			"url": "http://{address}:8080/", "method": "HEAD", "follow_redirects": false, "verify_tls": false,
			"body_contains": "ok", "timeout_s": 60.0,
		}, true},
		{"tls defaults", "tls", map[string]any{"port": 443.0, "timeout_s": 10.0}, true},
		{"tls no params", "tls", nil, true},
		{"tls server name", "tls", map[string]any{"port": 8443.0, "server_name": "example.com"}, true},
		{"dns defaults", "dns", map[string]any{"name": "{address}", "type": "A", "timeout_s": 5.0}, true},
		{"dns no params", "dns", nil, true},
		{"dns every type", "dns", map[string]any{"type": "AAAA"}, true},
		{"dns cname", "dns", map[string]any{"type": "CNAME"}, true},
		{"dns mx", "dns", map[string]any{"type": "MX"}, true},
		{"dns txt", "dns", map[string]any{"type": "TXT"}, true},
		{"dns ns", "dns", map[string]any{"type": "NS", "name": "example.com", "expect": "ns1.example.com."}, true},
		{"dns server ipv4", "dns", map[string]any{"server": "192.0.2.53"}, true},
		{"dns server ipv4 port", "dns", map[string]any{"server": "192.0.2.53:5353"}, true},
		{"dns server ipv6", "dns", map[string]any{"server": "2001:db8::53"}, true},
		{"dns server ipv6 port", "dns", map[string]any{"server": "[2001:db8::53]:53"}, true},
		{"dns server ipv6 bracketed", "dns", map[string]any{"server": "[2001:db8::53]"}, true},
		{"dns name trailing dot", "dns", map[string]any{"name": "example.com."}, true},
		{"dns name srv style", "dns", map[string]any{"name": "_sip._tcp.example.com"}, true},
		{"dns name with placeholder", "dns", map[string]any{"name": "www.{address}"}, true},
		{"dns name single label", "dns", map[string]any{"name": "intranet"}, true},
		{"dns name 253 bytes", "dns", map[string]any{"name": dnsName(253)}, true},
		{"dns name 63-byte label", "dns", map[string]any{"name": strings.Repeat("a", 63) + ".example"}, true},
		{"long string at the cap", "tcp", map[string]any{"port": 1.0, "expect": strings.Repeat("x", 500)}, true},
	})
}

// TestSyntheticParamsRefused, ported from internal/checks: every rule of the
// synthetic spec's Validation section, one case per way to break it.
// Unknown keys are refused on every plugin.
func TestSyntheticParamsRefused(t *testing.T) {
	runParamCases(t, []paramCase{
		// icmp
		{"icmp count 11", "icmp", map[string]any{"count": 11.0}, false},
		{"icmp count 0", "icmp", map[string]any{"count": 0.0}, false},
		{"icmp count fractional", "icmp", map[string]any{"count": 2.5}, false},
		{"icmp count as string", "icmp", map[string]any{"count": "3"}, false},
		{"icmp timeout 31", "icmp", map[string]any{"timeout_s": 31.0}, false},
		{"icmp unknown key", "icmp", map[string]any{"cnt": 3.0}, false},
		// tcp
		{"tcp without port", "tcp", map[string]any{"timeout_s": 5.0}, false},
		{"tcp no params", "tcp", nil, false},
		{"tcp port 70000", "tcp", map[string]any{"port": 70000.0}, false},
		{"tcp port 0", "tcp", map[string]any{"port": 0.0}, false},
		{"tcp port bool", "tcp", map[string]any{"port": true}, false},
		{"tcp empty expect", "tcp", map[string]any{"port": 22.0, "expect": ""}, false},
		{"tcp send not a string", "tcp", map[string]any{"port": 22.0, "send": 1.0}, false},
		{"tcp timeout 31", "tcp", map[string]any{"port": 22.0, "timeout_s": 31.0}, false},
		{"tcp string over the cap", "tcp", map[string]any{"port": 22.0, "expect": strings.Repeat("x", 501)}, false},
		{"tcp unknown key", "tcp", map[string]any{"port": 22.0, "banner": "x"}, false},
		// http
		{"http without url", "http", map[string]any{"method": "GET"}, false},
		{"http ftp url", "http", map[string]any{"url": "ftp://x"}, false},
		{"http no scheme", "http", map[string]any{"url": "example.com/x"}, false},
		{"http no host", "http", map[string]any{"url": "http:///path"}, false},
		{"http unparsable", "http", map[string]any{"url": "http://[::1"}, false},
		{"http placeholder in port", "http", map[string]any{"url": "http://example.com:{address}/"}, false},
		{"http userinfo", "http", map[string]any{"url": "https://user:pass@example.com/"}, false},
		{"http user only", "http", map[string]any{"url": "https://user@{address}/"}, false},
		{"http method POST", "http", map[string]any{"url": "http://x", "method": "POST"}, false},
		{"http method lower case", "http", map[string]any{"url": "http://x", "method": "get"}, false},
		{"http follow_redirects not bool", "http", map[string]any{"url": "http://x", "follow_redirects": "yes"}, false},
		{"http verify_tls not bool", "http", map[string]any{"url": "http://x", "verify_tls": 1.0}, false},
		{"http empty body_contains", "http", map[string]any{"url": "http://x", "body_contains": ""}, false},
		{"http timeout 61", "http", map[string]any{"url": "http://x", "timeout_s": 61.0}, false},
		{"http url over the cap", "http", map[string]any{"url": "http://x/" + strings.Repeat("a", 500)}, false},
		{"http unknown key", "http", map[string]any{"url": "http://x", "headers": map[string]any{}}, false},
		// tls
		{"tls port 70000", "tls", map[string]any{"port": 70000.0}, false},
		{"tls empty server_name", "tls", map[string]any{"server_name": ""}, false},
		{"tls blank server_name", "tls", map[string]any{"server_name": "  "}, false},
		{"tls timeout 31", "tls", map[string]any{"timeout_s": 31.0}, false},
		{"tls unknown key", "tls", map[string]any{"sni": "x"}, false},
		// dns
		{"dns type SRV", "dns", map[string]any{"type": "SRV"}, false},
		{"dns type lower case", "dns", map[string]any{"type": "a"}, false},
		{"dns server not an ip", "dns", map[string]any{"server": "not-an-ip"}, false},
		{"dns server hostname port", "dns", map[string]any{"server": "dns.example:53"}, false},
		{"dns server port 0", "dns", map[string]any{"server": "192.0.2.53:0"}, false},
		{"dns server port 70000", "dns", map[string]any{"server": "192.0.2.53:70000"}, false},
		{"dns server garbage after an ipv6 address", "dns", map[string]any{"server": "2001:db8::53:53:x"}, false},
		{"dns server unclosed bracket", "dns", map[string]any{"server": "[2001:db8::53"}, false},
		{"dns server bracketed ipv4", "dns", map[string]any{"server": "[192.0.2.53]"}, false},
		{"dns empty name", "dns", map[string]any{"name": ""}, false},
		{"dns name with a space", "dns", map[string]any{"name": "exa mple.com"}, false},
		{"dns name with an at sign", "dns", map[string]any{"name": "ex@mple.com"}, false},
		{"dns name empty label", "dns", map[string]any{"name": "example..com"}, false},
		{"dns name leading dot", "dns", map[string]any{"name": ".example.com"}, false},
		{"dns name root only", "dns", map[string]any{"name": "."}, false},
		{"dns name two trailing dots", "dns", map[string]any{"name": "example.com.."}, false},
		{"dns name placeholder with a space", "dns", map[string]any{"name": "{address} x"}, false},
		{"dns name 254 bytes", "dns", map[string]any{"name": dnsName(254)}, false},
		{"dns name 64-byte label", "dns", map[string]any{"name": strings.Repeat("a", 64) + ".example"}, false},
		{"dns name not a string", "dns", map[string]any{"name": 5.0}, false},
		{"dns empty expect", "dns", map[string]any{"expect": ""}, false},
		{"dns timeout 31", "dns", map[string]any{"timeout_s": 31.0}, false},
		{"dns unknown key", "dns", map[string]any{"qtype": "A"}, false},
	})
}
