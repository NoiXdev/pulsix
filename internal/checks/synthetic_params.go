package checks

import (
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

// IsSynthetic reports whether plugin is one of the synthetic plugins
// (backlog 3.13) the manager probes itself. Such a check never goes to an
// agent: the agent does not know these plugins and would report "unknown
// plugin" (spec 2026-11-11-synthetic-checks-design.md, "Where they run").
func IsSynthetic(plugin string) bool {
	switch plugin {
	case PluginICMP, PluginTCP, PluginHTTP, PluginTLS, PluginDNS:
		return true
	}
	return false
}

// maxParamStringLen bounds every string parameter of a synthetic plugin.
// No other plugin's params carried a length bound before; 500 bytes holds
// any URL, host name, banner or answer a probe reasonably checks for while
// keeping a definition from smuggling kilobytes into every probe and every
// agent-independent config read.
const maxParamStringLen = 500

// paramRule validates one parameter's value; it returns a description of
// what the value must be, or "" when it is valid.
type paramRule func(v any) string

// syntheticSchema is the spec's Plugins table: per plugin, the parameters
// it accepts and how each is checked. Every parameter is optional except
// the ones syntheticRequired names. internal/probe trusts params that pass
// here, so the two must agree — ranges, enums and the {address}
// placeholder included.
var syntheticSchema = map[string]map[string]paramRule{
	PluginICMP: {
		"count":     paramWhole(1, 10),
		"timeout_s": paramWhole(1, 30),
	},
	PluginTCP: {
		"port":      paramWhole(1, 65535),
		"send":      paramText(true),
		"expect":    paramText(false),
		"timeout_s": paramWhole(1, 30),
	},
	PluginHTTP: {
		"url":              paramHTTPURL,
		"method":           paramOneOf("GET", "HEAD"),
		"follow_redirects": paramBool,
		"verify_tls":       paramBool,
		"body_contains":    paramText(false),
		"timeout_s":        paramWhole(1, 60),
	},
	PluginTLS: {
		"port":        paramWhole(1, 65535),
		"server_name": paramName,
		"timeout_s":   paramWhole(1, 30),
	},
	PluginDNS: {
		"name":      paramDNSName,
		"type":      paramOneOf("A", "AAAA", "CNAME", "MX", "TXT", "NS"),
		"server":    paramDNSServer,
		"expect":    paramText(false),
		"timeout_s": paramWhole(1, 30),
	},
}

// syntheticRequired names the parameters a synthetic plugin cannot run
// without: tcp has no default port, http no default URL.
var syntheticRequired = map[string][]string{
	PluginTCP:  {"port"},
	PluginHTTP: {"url"},
}

// validateSyntheticParams checks params against plugin's row of
// syntheticSchema: no unknown key, every required key present, every
// value valid. Keys are visited in sorted order so the same bad input
// always names the same parameter.
func validateSyntheticParams(plugin string, params map[string]any) error {
	schema := syntheticSchema[plugin]
	for _, k := range syntheticRequired[plugin] {
		if _, ok := params[k]; !ok {
			return fmt.Errorf("%w: %s: %s is required", ErrInvalidInput, plugin, k)
		}
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rule, ok := schema[k]
		if !ok {
			return fmt.Errorf("%w: %s: unknown parameter %q", ErrInvalidInput, plugin, k)
		}
		if want := rule(params[k]); want != "" {
			return fmt.Errorf("%w: %s: %s must be %s", ErrInvalidInput, plugin, k, want)
		}
	}
	return nil
}

// wholeNumber reads a whole number out of a value a JSON body decodes as
// float64 (or a Go caller passes as an int).
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// paramWhole accepts a whole number in [lo, hi].
func paramWhole(lo, hi int64) paramRule {
	return func(v any) string {
		if n, ok := wholeNumber(v); ok && n >= lo && n <= hi {
			return ""
		}
		return fmt.Sprintf("a whole number between %d and %d", lo, hi)
	}
}

// paramText accepts a string of at most maxParamStringLen bytes, the empty
// string only when allowEmpty is set.
func paramText(allowEmpty bool) paramRule {
	return func(v any) string {
		s, ok := v.(string)
		if !ok || len(s) > maxParamStringLen || (!allowEmpty && s == "") {
			if allowEmpty {
				return fmt.Sprintf("a string of at most %d bytes", maxParamStringLen)
			}
			return fmt.Sprintf("a non-empty string of at most %d bytes", maxParamStringLen)
		}
		return ""
	}
}

// paramName accepts a host or DNS name: a string that is not blank, of at most
// maxParamStringLen bytes. It may contain the {address} placeholder.
func paramName(v any) string {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" && len(s) <= maxParamStringLen {
		return ""
	}
	return fmt.Sprintf("a non-blank string of at most %d bytes", maxParamStringLen)
}

// dnsPlaceholderLabel stands in for {address} while a dns name is checked.
const dnsPlaceholderLabel = "address"

// paramDNSName accepts a DNS name, after {address} is replaced with a
// placeholder label: dot-separated labels of letters, digits, '-' and '_'
// (service names such as _sip._tcp), each 1–63 bytes, an optional
// trailing dot, at most 253 bytes without it. A name that cannot be one
// would only ever read dns.up = 0. web/src/features/checks/syntheticParams.ts
// mirrors this rule.
func paramDNSName(v any) string {
	const want = "a DNS name: dot-separated labels of letters, digits, hyphens and underscores, at most 253 bytes (it may contain {address})"
	s, ok := v.(string)
	if !ok || len(s) > maxParamStringLen || !validDNSName(strings.ReplaceAll(s, "{address}", dnsPlaceholderLabel)) {
		return want
	}
	return ""
}

// validDNSName is paramDNSName's rule for a name without placeholders.
func validDNSName(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return false
			}
		}
	}
	return true
}

// paramOneOf accepts exactly one of values, case included: the probe sends the
// method and asks for the record type verbatim.
func paramOneOf(values ...string) paramRule {
	return func(v any) string {
		if s, ok := v.(string); ok {
			for _, want := range values {
				if s == want {
					return ""
				}
			}
		}
		return "one of " + strings.Join(values, ", ")
	}
}

// paramBool accepts true or false.
func paramBool(v any) string {
	if _, ok := v.(bool); ok {
		return ""
	}
	return "true or false"
}

// urlPlaceholderHost stands in for {address} while a URL is checked; the
// reserved .invalid TLD can never name a real host.
const urlPlaceholderHost = "address.invalid"

// paramHTTPURL accepts an http or https URL with a host, after {address}
// is replaced with a placeholder host name — the same parse the probe runs
// after replacing it with the host's real address. A URL carrying user
// info (user:pass@) is refused: credentials have no place in a check
// definition every operator of the organization can read.
func paramHTTPURL(v any) string {
	const want = "an http:// or https:// URL with a host and no user info"
	s, ok := v.(string)
	if !ok || len(s) > maxParamStringLen {
		return fmt.Sprintf("%s, of at most %d bytes", want, maxParamStringLen)
	}
	u, err := url.Parse(strings.ReplaceAll(s, "{address}", urlPlaceholderHost))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return want
	}
	return ""
}

// paramDNSServer accepts an IP address with an optional port: 192.0.2.53,
// 192.0.2.53:5353, 2001:db8::53, [2001:db8::53] or [2001:db8::53]:53. A
// port, when given, is 1–65535.
func paramDNSServer(v any) string {
	const want = "an IP address with an optional port (an IPv6 address with a port in brackets)"
	s, ok := v.(string)
	if !ok || len(s) > maxParamStringLen {
		return want
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		if ap.Port() == 0 {
			return want
		}
		return ""
	}
	if inner, bracketed := strings.CutPrefix(s, "["); bracketed {
		inner, closed := strings.CutSuffix(inner, "]")
		if ip, err := netip.ParseAddr(inner); closed && err == nil && ip.Is6() {
			return ""
		}
		return want
	}
	if _, err := netip.ParseAddr(s); err != nil {
		return want
	}
	return ""
}
