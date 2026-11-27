package secrets

import (
	"net"
	"net/netip"
	"strconv"

	"gopkg.in/yaml.v3"
)

// StatusPage is the status_page section of the file (backlog 3.132): where
// the agent's local, read-only status page listens and how it is
// protected. The zero value is the default: the page is off.
type StatusPage struct {
	// Listen is the address the page binds, host:port; empty turns the
	// page off. "localhost" is written as 127.0.0.1, so a hosts file
	// cannot move a loopback page onto the network.
	Listen string
	// Token is what a visitor presents; required when Network.
	Token string
	// TLSCert and TLSKey are a certificate and key to serve HTTPS with;
	// both or neither.
	TLSCert string
	TLSKey  string
	// Network is true when Listen is not a loopback address: the page is
	// then served over HTTPS only, and only with a token.
	Network bool
}

// On reports whether the section turns the page on.
func (p StatusPage) On() bool { return p.Listen != "" }

// Limits of the section.
const (
	minStatusTokenLen = 32
	maxStatusTokenLen = 1024
	maxStatusPathLen  = 4096
)

const (
	badStatusPage        = "status_page must map listen, token, tls_cert and tls_key to their values"
	badStatusPageKey     = "unknown key under status_page; only listen, token, tls_cert and tls_key are allowed"
	badStatusPageValue   = "a status_page value must be a single value, not a list or mapping"
	badStatusListen      = "listen must be host:port with a port from 1 to 65535, such as 127.0.0.1:8742"
	badStatusToken       = "token must be 32 to 1024 characters"
	badStatusNoToken     = "a listen address that is not loopback needs a token of at least 32 characters"
	badStatusTLSPair     = "tls_cert and tls_key must be set together"
	badStatusPathTooLong = "a status_page path is longer than 4096 bytes"
)

// statusPageKeys are the section's keys.
var statusPageKeys = map[string]bool{"listen": true, "token": true, "tls_cert": true, "tls_key": true}

// parseStatusPage decodes the mapping under status_page:
//
//	status_page:
//	  listen: <host:port>
//	  token: <32 to 1024 characters>
//	  tls_cert: <path>
//	  tls_key: <path>
//
// Unlike the other sections, a mistake here never refuses the file: it
// turns the page off and is returned as problem, "line N, column M:
// <reason>", which never quotes what was written (R1 of the 3.132 plan).
// A missing section or a missing listen is the page off, without a
// problem.
func parseStatusPage(n *yaml.Node) (page StatusPage, problem string) {
	if n == nil || isNull(n) {
		return StatusPage{}, ""
	}
	if err := checkNodes(n); err != nil {
		return StatusPage{}, err.Error()
	}
	if n.Kind != yaml.MappingNode {
		return StatusPage{}, errAt(n, badStatusPage).Error()
	}
	vals := map[string]*yaml.Node{}
	keys := map[string]*yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode || !statusPageKeys[key.Value] {
			return StatusPage{}, errAt(key, badStatusPageKey).Error()
		}
		if prev, dup := keys[key.Value]; dup {
			return StatusPage{}, errAt(key, key.Value+" is already set at line "+strconv.Itoa(prev.Line)).Error()
		}
		if isNull(val) {
			continue // an empty value is the key left out
		}
		if val.Kind != yaml.ScalarNode {
			return StatusPage{}, errAt(val, badStatusPageValue).Error()
		}
		keys[key.Value], vals[key.Value] = key, val
	}
	str := func(k string) string {
		if v := vals[k]; v != nil {
			return v.Value
		}
		return ""
	}
	page = StatusPage{Token: str("token"), TLSCert: str("tls_cert"), TLSKey: str("tls_key")}
	if v := vals["token"]; v != nil && (len(page.Token) < minStatusTokenLen || len(page.Token) > maxStatusTokenLen) {
		return StatusPage{}, errAt(v, badStatusToken).Error()
	}
	for _, k := range []string{"tls_cert", "tls_key"} {
		if v := vals[k]; v != nil && len(v.Value) > maxStatusPathLen {
			return StatusPage{}, errAt(v, badStatusPathTooLong).Error()
		}
	}
	if (page.TLSCert == "") != (page.TLSKey == "") {
		at := keys["tls_cert"]
		if at == nil {
			at = keys["tls_key"]
		}
		return StatusPage{}, errAt(at, badStatusTLSPair).Error()
	}
	lv := vals["listen"]
	if lv == nil {
		return StatusPage{}, "" // no listen: the page is off
	}
	listen, network, ok := parseListen(lv.Value)
	if !ok {
		return StatusPage{}, errAt(lv, badStatusListen).Error()
	}
	if network && page.Token == "" {
		return StatusPage{}, errAt(lv, badStatusNoToken).Error()
	}
	page.Listen, page.Network = listen, network
	return page, ""
}

// parseListen checks a host:port and says whether it is a network
// address. Loopback is localhost (rewritten as 127.0.0.1), 127.0.0.0/8 and
// ::1; anything else — an empty host (every interface), another address,
// a name — is network.
func parseListen(s string) (listen string, network, ok bool) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", false, false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port {
		return "", false, false
	}
	if host == "localhost" {
		return net.JoinHostPort("127.0.0.1", port), false, true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return net.JoinHostPort(a.String(), port), !a.Unmap().IsLoopback() || a.Zone() != "", true
	}
	return net.JoinHostPort(host, port), true, true
}
