package assert

import (
	"slices"
	"strings"
)

// Dialect is one plugin's assertion vocabulary (backlog 3.87, 3.134): the
// subjects its probe can judge. The entry syntax, the operators and the
// bounds are the same in every dialect, and a subject means the same in
// every dialect that has it (response_ms, cert_days, cert_issuer), so a
// subject's kind (Numeric, its operators) does not depend on the dialect.
type Dialect struct {
	plugin   string
	subjects []Subject
}

// The dialects. HTTP is what Parse parses.
var (
	// HTTP: backlog 3.13b. html("…") count is spelled with html.
	HTTP = Dialect{plugin: "http", subjects: []Subject{
		SubjectStatus, SubjectResponseMS, SubjectTTFBMS, SubjectSize, SubjectRedirects,
		SubjectCertDays, SubjectCertIssuer, SubjectBody, SubjectHeader, SubjectJSON, SubjectHTML,
	}}
	// TCP: the connect time, and the first line the target sent with the
	// time it took to arrive.
	TCP = Dialect{plugin: "tcp", subjects: []Subject{SubjectConnectMS, SubjectResponseMS, SubjectResponse}}
	// DNS: the lookup time and the answers.
	DNS = Dialect{plugin: "dns", subjects: []Subject{SubjectResolveMS, SubjectAnswerCount, SubjectAnswers, SubjectAnswer}}
	// TLS: the handshake and the leaf certificate.
	TLS = Dialect{plugin: "tls", subjects: []Subject{
		SubjectHandshakeMS, SubjectCertValid, SubjectCertDays, SubjectCertIssuer,
		SubjectCertSubject, SubjectCertSANs, SubjectVersion, SubjectCipher,
	}}
	// SNMP (backlog 3.134): an oids entry's value, the first request's
	// round trip and the device's uptime in seconds.
	SNMP = Dialect{plugin: "snmp", subjects: []Subject{SubjectValue, SubjectResponseMS, SubjectUptime}}
)

var dialects = []Dialect{HTTP, TCP, DNS, TLS, SNMP}

// DialectOf is plugin's dialect; false for a plugin without assertions.
func DialectOf(plugin string) (Dialect, bool) {
	i := slices.IndexFunc(dialects, func(d Dialect) bool { return d.plugin == plugin })
	if i < 0 {
		return Dialect{}, false
	}
	return dialects[i], true
}

// Plugins are the plugins with assertions, in a fixed order.
func Plugins() []string {
	out := make([]string, len(dialects))
	for i, d := range dialects {
		out[i] = d.plugin
	}
	return out
}

// Plugin is the plugin the dialect belongs to.
func (d Dialect) Plugin() string { return d.plugin }

// Subjects are the dialect's subjects in the order its documentation
// lists them (html count is not listed apart from html).
func (d Dialect) Subjects() []Subject { return slices.Clone(d.subjects) }

// knows reports whether s is one of the dialect's subjects.
func (d Dialect) knows(s Subject) bool { return slices.Contains(d.subjects, s) }

// subjectList is the dialect's subjects for an error message.
func (d Dialect) subjectList() string {
	names := make([]string, len(d.subjects))
	for i, s := range d.subjects {
		names[i] = string(s)
	}
	return strings.Join(names, ", ")
}
