package checkplugins

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/noixdev/pulsix/internal/jpath"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/httpreq"
)

// Format names a validator for a string, or for each entry of a list. The
// SPA has a TypeScript twin of every format; testdata/formats.json pins
// that both sides agree. A format checks shape only: a parameter's MaxBytes
// bounds its length separately.
type Format string

const (
	// FormatNotBlank: not empty and not only whitespace.
	FormatNotBlank Format = "notBlank"
	// FormatNoControl: no control characters (Unicode category Cc). It
	// accepts "" — a list refuses empty entries on its own.
	FormatNoControl Format = "noControl"
	// FormatDevicePath: a device path smartctl is handed — under /dev/, only
	// characters that mean nothing to a shell or an option parser, no ".."
	// segment (which would lead out of /dev/).
	FormatDevicePath Format = "devicePath"
	// FormatURL: an http:// or https:// URL with a host and no user info,
	// once {address} stands for a host name.
	FormatURL Format = "url"
	// FormatStatusURL: an http:// or https:// URL with a host, no user info,
	// no fragment and no {address} placeholder — the agent fetches it as
	// written (the web server status checks, backlog 3.11).
	FormatStatusURL Format = "statusURL"
	// FormatPHPFPMPool: one entry of phpfpm's urls — a FormatStatusURL, or
	// the pool's FastCGI address with its status path,
	// fcgi://host:port/path or unix:/socket:/path (backlog 3.11a, see
	// phpfpm_pool.go).
	FormatPHPFPMPool Format = "phpfpmPool"
	// FormatDNSName: a DNS name, once {address} stands for a label.
	FormatDNSName Format = "dnsName"
	// FormatServer: an IP address with an optional port, an IPv6 address
	// with a port in brackets.
	FormatServer Format = "server"
	// FormatDockerLabel: a container label key, optionally =value.
	FormatDockerLabel Format = "dockerLabel"
	// FormatDockerSocket: an absolute path or a Windows named pipe.
	FormatDockerSocket Format = "dockerSocket"
	// FormatSecretName: the name of a secret in the agent's local
	// configuration file (agent.yaml, backlog 3.5) — 1 to 64 letters,
	// digits, '_', '.' or '-', the agent's own rule for the names it
	// loads.
	FormatSecretName Format = "secretName"
	// FormatCommandName: the name of a command in the agent's local
	// configuration file (agent.yaml's commands:, backlog 3.6g) — the same
	// rule as FormatSecretName, a format of its own so the API and the SPA
	// explain a refusal as a command name rather than a secret's.
	FormatCommandName Format = "commandName"
	// FormatSNMPOID: one entry of snmp's oids (backlog 3.13d), "name=OID".
	// The name follows FormatSecretName's rule and becomes the item the
	// value is reported on; the OID is numeric and dotted (1.3.6.1.2.1.1.3.0,
	// a leading dot allowed), 2 to 128 arcs, each a decimal of at most
	// 2^32-1 without leading zeros. Symbolic names need MIB files, which
	// the manager does not load.
	FormatSNMPOID Format = "snmpOID"
	// FormatJSONValue: one entry of http's json_values (backlog 3.6h),
	// "name=path". The name follows FormatSecretName's rule and becomes the
	// item the value is reported on; the path is at most 512 bytes and in
	// the bounded RFC 9535 subset internal/jpath parses, the parser the
	// probe evaluates it with. The SPA's twin checks the shape, the length
	// and the descendant segments (..) only.
	FormatJSONValue Format = "jsonValue"
	// FormatAssertion: one entry of http's assertions (backlog 3.13b),
	// "name=expression" with an optional " @warn" or " @crit", in the
	// grammar internal/probe/assert parses, the parser the probe evaluates
	// it with. The name follows FormatSecretName's rule and becomes the
	// item the assertion reports on. The SPA's twin checks the grammar but
	// leaves regular expressions, CSS selectors and JSONPaths beyond their
	// shape to the server.
	FormatAssertion Format = "assertion"
	// FormatUUID: a canonical lower-case UUID (8-4-4-4-12 hex digits, no
	// braces or urn prefix) — the id of a monitoring group in a check's
	// vantage_groups (backlog 3.13a).
	FormatUUID Format = "uuid"
	// FormatHTTPHeader: one entry of http's headers (backlog 3.84), "Name:
	// value" as internal/probe/httpreq reads it: the name a token, not one
	// the probe controls nor one that carries credentials, the value
	// without control characters other than tab.
	FormatHTTPHeader Format = "httpHeader"
	// FormatMediaType: http's content_type (backlog 3.84), type/subtype
	// with optional ;name=value parameters (internal/probe/httpreq).
	FormatMediaType Format = "mediaType"
)

// Formats returns every format a validator knows, in declaration order.
// Tests pin that each one has a vector in testdata/formats.json (and so a
// TypeScript twin) and a phrase in the API's params doc.
func Formats() []Format {
	return []Format{
		FormatNotBlank, FormatNoControl, FormatDevicePath, FormatURL, FormatStatusURL,
		FormatPHPFPMPool, FormatDNSName, FormatServer, FormatDockerLabel, FormatDockerSocket,
		FormatSecretName, FormatCommandName, FormatSNMPOID, FormatJSONValue, FormatAssertion, FormatUUID,
		FormatHTTPHeader, FormatMediaType,
	}
}

// Valid reports whether s passes the format. An unknown format accepts
// nothing.
func (f Format) Valid(s string) bool { return f.problem(s) == "" }

// problem describes why s fails the format, phrased to follow the
// parameter's name ("must be …"), or returns "" when it passes.
func (f Format) problem(s string) string {
	switch f {
	case FormatNotBlank:
		if strings.TrimSpace(s) == "" {
			return "must not be blank"
		}
	case FormatNoControl:
		if hasControl(s) {
			return "must not contain control characters"
		}
	case FormatDevicePath:
		return devicePathProblem(s)
	case FormatURL:
		if !validProbeURL(s) {
			return "must be an http:// or https:// URL with a host and no user info"
		}
	case FormatStatusURL:
		if !validStatusURL(s) {
			return "must be an http:// or https:// URL with a host, no user info and no fragment (it must not contain {address})"
		}
	case FormatPHPFPMPool:
		return phpfpmPoolProblem(s)
	case FormatDNSName:
		if !validDNSName(strings.ReplaceAll(s, "{address}", dnsPlaceholderLabel)) {
			return "must be a DNS name: dot-separated labels of letters, digits, hyphens and underscores, at most 253 bytes (it may contain {address})"
		}
	case FormatServer:
		if !validDNSServer(s) {
			return "must be an IP address with an optional port (an IPv6 address with a port in brackets)"
		}
	case FormatDockerLabel:
		return dockerLabelProblem(s)
	case FormatDockerSocket:
		return dockerSocketProblem(s)
	case FormatSecretName, FormatCommandName:
		if !secretNamePattern.MatchString(s) {
			return "must be 1 to 64 letters, digits, '_', '.' or '-'"
		}
	case FormatSNMPOID:
		return snmpOIDProblem(s)
	case FormatJSONValue:
		return jsonValueProblem(s)
	case FormatAssertion:
		return assertionProblem(s)
	case FormatUUID:
		if id, err := uuid.Parse(s); err != nil || id.String() != s {
			return "must be a lower-case UUID like 123e4567-e89b-12d3-a456-426614174000"
		}
	case FormatHTTPHeader:
		return httpreq.HeaderProblem(s)
	case FormatMediaType:
		return httpreq.MediaTypeProblem(s)
	default:
		return "uses a format no validator knows: " + string(f)
	}
	return ""
}

// secretNamePattern is the shape of a secret's or a command's name in
// agent.yaml; the agent (internal/agent's secrets file) refuses every other
// name with the same rule, and the manager's heartbeat store
// (internal/agents) applies this format to the secret names an agent
// reports.
var secretNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func hasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }

// devicePattern is the shape of a device path smartctl is handed: always
// under /dev/ (smartctl names devices /dev/sdX, /dev/nvmeN, /dev/pdN and
// /dev/csmiN,M on Windows as well).
var devicePattern = regexp.MustCompile(`^/dev/[A-Za-z0-9._/,:+@-]+$`)

func devicePathProblem(s string) string {
	switch {
	case hasControl(s):
		return "must not contain control characters"
	case !devicePattern.MatchString(s):
		return "must start with /dev/ and contain only letters, digits and . _ / , : + @ -"
	case slices.Contains(strings.Split(s, "/"), ".."):
		return "must not contain a .. path segment"
	}
	return ""
}

// urlPlaceholderHost stands in for {address} while a URL is checked; the
// reserved .invalid TLD can never name a real host. The probe runs the
// same parse after replacing {address} with the host's real address.
const urlPlaceholderHost = "address.invalid"

// validProbeURL: user info (user:pass@) is refused because credentials have
// no place in a check definition every operator of the organization can
// read.
func validProbeURL(s string) bool {
	u, err := url.Parse(strings.ReplaceAll(s, "{address}", urlPlaceholderHost))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil
}

// validStatusURL: a probe URL that is fetched as written, so no {address}
// placeholder, and no '#' anywhere (a fragment is never sent and would only
// hide a typo).
func validStatusURL(s string) bool {
	return !strings.Contains(s, "{address}") && !strings.Contains(s, "#") && validProbeURL(s)
}

// dnsPlaceholderLabel stands in for {address} while a DNS name is checked.
const dnsPlaceholderLabel = "address"

// validDNSName: dot-separated labels of letters, digits, '-' and '_'
// (service names such as _sip._tcp), each 1–63 bytes, an optional trailing
// dot, at most 253 bytes without it. A name that cannot be one would only
// ever read dns.up = 0.
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

// validDNSServer accepts 192.0.2.53, 192.0.2.53:5353, 2001:db8::53,
// [2001:db8::53] and [2001:db8::53]:53. A port, when given, is 1–65535.
func validDNSServer(s string) bool {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Port() != 0
	}
	if inner, bracketed := strings.CutPrefix(s, "["); bracketed {
		inner, closed := strings.CutSuffix(inner, "]")
		ip, err := netip.ParseAddr(inner)
		return closed && err == nil && ip.Is6()
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// Docker label bounds (backlog 3.12). The agent trusts params that pass
// here, so these and internal/agent/checks/plugins/docker/ agree.
const (
	maxDockerLabelKeyLen   = 255
	maxDockerLabelValueLen = 255
)

// dockerLabelKey is the shape of a container label key the plugin selects on.
var dockerLabelKey = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

// dockerLabelProblem splits at the first '='; the value may be empty.
func dockerLabelProblem(s string) string {
	key, value, _ := strings.Cut(s, "=")
	switch {
	case !dockerLabelKey.MatchString(key):
		return fmt.Sprintf("key must be 1 to %d characters of letters, digits, '.', '_', '/' or '-'", maxDockerLabelKeyLen)
	case len(value) > maxDockerLabelValueLen:
		return fmt.Sprintf("value must be at most %d bytes", maxDockerLabelValueLen)
	case hasControl(value):
		return "value must not contain control characters"
	}
	return ""
}

// dockerPipePrefix is how a Windows named pipe is spelled to the engine
// client.
const dockerPipePrefix = `npipe:////./pipe/`

func dockerSocketProblem(s string) string {
	if hasControl(s) {
		return "must not contain control characters"
	}
	isPath := strings.HasPrefix(s, "/")
	isPipe := strings.HasPrefix(s, dockerPipePrefix) && len(s) > len(dockerPipePrefix)
	if !isPath && !isPipe {
		return "must be an absolute path or " + dockerPipePrefix + " followed by a pipe name"
	}
	return ""
}

// SNMP OID bounds: an OID has at least two arcs and at most 128 (RFC 2578's
// limit on sub-identifiers), each an unsigned 32-bit number.
const (
	minSNMPOIDArcs = 2
	maxSNMPOIDArcs = 128
	maxSNMPOIDArc  = 1<<32 - 1
)

// snmpOIDProblem checks a "name=OID" entry, split at the first '='.
func snmpOIDProblem(s string) string {
	name, oid, ok := strings.Cut(s, "=")
	if !ok || !secretNamePattern.MatchString(name) {
		return "must be name=OID, the name 1 to 64 letters, digits, '_', '.' or '-'"
	}
	if !validNumericOID(oid) {
		return "must be name=OID, the OID numeric and dotted such as 1.3.6.1.2.1.1.3.0: 2 to 128 whole numbers, each at most 4294967295 and without leading zeros, the first 0, 1 or 2, the second at most 39 after 0 or 1 and at most 4294967215 after 2, an optional leading dot"
	}
	return ""
}

// validNumericOID: dot-separated decimal arcs, one leading dot allowed,
// and only OIDs BER can encode (X.690 8.19): the first two arcs travel as
// one subidentifier 40*first+second, so the first is 0, 1 or 2, the
// second at most 39 under 0 and 1, and the sum at most maxSNMPOIDArc —
// exactly what the probe's SNMP library refuses to encode otherwise.
func validNumericOID(oid string) bool {
	arcs := strings.Split(strings.TrimPrefix(oid, "."), ".")
	if len(arcs) < minSNMPOIDArcs || len(arcs) > maxSNMPOIDArcs {
		return false
	}
	vals := make([]uint64, len(arcs))
	for i, arc := range arcs {
		v, ok := oidArc(arc)
		if !ok {
			return false
		}
		vals[i] = v
	}
	first, second := vals[0], vals[1]
	return first <= 2 && (first == 2 || second <= 39) && 40*first+second <= maxSNMPOIDArc
}

// oidArc reads "0", or ASCII digits without a leading zero, at most
// maxSNMPOIDArc.
func oidArc(arc string) (uint64, bool) {
	if arc == "" || len(arc) > 10 || (arc[0] == '0' && arc != "0") {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(arc); i++ {
		c := arc[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, n <= maxSNMPOIDArc
}

// maxJSONPathLen bounds a json_values entry's path in bytes (backlog 3.6h).
const maxJSONPathLen = 512

// jsonValueProblem checks a "name=path" entry, split at the first '='
// (a name never contains one; a path may, in a filter).
func jsonValueProblem(s string) string {
	name, path, ok := strings.Cut(s, "=")
	if !ok || !secretNamePattern.MatchString(name) {
		return "must be name=JSONPath, the name 1 to 64 letters, digits, '_', '.' or '-'"
	}
	if !strings.HasPrefix(path, "$") || len(path) > maxJSONPathLen {
		return fmt.Sprintf("must be name=JSONPath, the path starting with $ and at most %d bytes", maxJSONPathLen)
	}
	if _, err := jpath.Parse(path); err != nil {
		// The parser's own reason: the path is the operator's
		// configuration, never a response, so quoting it is safe.
		return "must be name=JSONPath, the path a JSONPath in Pulsix's supported subset (see the docs): " + err.Error()
	}
	return ""
}

// assertionProblem checks one assertions entry with the probe's parser,
// naming the entry and the parser's reason: the entry is the operator's
// configuration, never a response, so quoting it is safe.
func assertionProblem(s string) string {
	if _, err := assert.Parse(s); err != nil {
		return fmt.Sprintf("must be name=assertion such as ok=status == 200 (see the docs): %q: %s", s, err)
	}
	return ""
}
