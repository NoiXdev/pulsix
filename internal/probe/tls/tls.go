package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/judge"
)

const (
	tlsDefaultTimeout = 10 * time.Second
	tlsDefaultPort    = 443
)

// probeTLS completes a TLS handshake with address:port without trusting the
// certificate, then verifies the chain itself against roots (nil: the
// system roots) and server_name ({address} replaced), so the leaf's expiry
// and issuer are reported for an invalid chain too.
//
// Each assertion is judged on an up attempt (backlog 3.87).
func probeTLS(ctx context.Context, g *probe.Guard, params map[string]any, address string, roots *x509.CertPool) []probe.Observation {
	entries := judge.Entries(assert.TLS, params)

	host, err := probe.TargetHost(address)
	if err != nil {
		return failAll(entries, err.Error())
	}
	port, ok := probe.PortParam(params, tlsDefaultPort)
	if !ok {
		return failAll(entries, probe.ErrBadPort.Error())
	}
	// server_name may carry {address}: it becomes the host's address
	// without IPv6 brackets, the form SNI and verification take.
	serverName := strings.ReplaceAll(probe.StrParam(params, "server_name", ""), probe.AddressPlaceholder, host)
	if serverName == "" {
		serverName = host
	}

	ctx, cancel := probe.Bound(ctx, params, tlsDefaultTimeout)
	defer cancel()

	// refused sees every address of a name the guard refuses: the dial
	// returns only one address's error.
	var refused probe.Refusal
	raw, err := g.DialerNoting(0, &refused).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if refused.Note(err); refused.Refused() {
			return failAll(entries, refused.Text())
		}
		return probe.Down("tls", probe.Classify(err))
	}
	conn := tls.Client(raw, tlsProbeConfig(serverName))
	defer func() { _ = conn.Close() }()

	start := time.Now()
	if err := conn.HandshakeContext(ctx); err != nil {
		return probe.Down("tls", probe.ClassifyHandshake(err))
	}
	// An invalid chain is no failure: the handshake completed, and
	// tls.cert_valid says the rest.
	state := conn.ConnectionState()
	h := handshake{elapsed: time.Since(start), version: state.Version, cipher: state.CipherSuite, at: time.Now()}
	obs := append(probe.Up("tls"), probe.Value("tls.handshake_ms", probe.Millis(h.elapsed)))

	certs := state.PeerCertificates
	if len(certs) == 0 {
		obs = append(obs, probe.Value("tls.cert_valid", 0))
		return append(obs, judge.Observe("tls", entries, h.judge)...)
	}
	h.leaf = certs[0]
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	_, verr := h.leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		DNSName:       serverName,
		CurrentTime:   h.at,
	})
	h.valid = verr == nil
	obs = append(obs,
		probe.BoolValue("tls.cert_valid", h.valid),
		probe.Value("tls.cert_expiry_days", h.days()),
		probe.Text("tls.cert_issuer", probe.IssuerLabel(h.leaf)),
	)
	return append(obs, judge.Observe("tls", entries, h.judge)...)
}

// hostMetrics are the whole-host metrics an up attempt reports.
var hostMetrics = []string{"tls.up", "tls.failure", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days", "tls.cert_issuer"}

// FailAll is the result of a tls probe of params that could not be
// attempted: msg on its whole-host metrics and on both metrics of each
// assertion's item.
func FailAll(params map[string]any, msg string) []probe.Observation {
	return failAll(judge.Entries(assert.TLS, params), msg)
}

func failAll(entries []judge.Entry, msg string) []probe.Observation {
	return append(probe.FailAll(msg, hostMetrics...), judge.FailAll("tls", entries, msg)...)
}

// errNoCertificate: the handshake completed without a certificate, so
// the certificate's subjects cannot be judged.
const errNoCertificate = "the server sent no certificate"

// handshake is what an up attempt learned (backlog 3.87): the time, the
// protocol version and cipher suite, and the leaf certificate (nil when
// none was sent) with the chain's verdict.
type handshake struct {
	elapsed time.Duration
	version uint16
	cipher  uint16
	at      time.Time
	leaf    *x509.Certificate
	valid   bool
}

// days is the leaf's days until NotAfter.
func (h handshake) days() float64 { return h.leaf.NotAfter.Sub(h.at).Hours() / 24 }

// judge evaluates one of tls's subjects.
func (h handshake) judge(a assert.Assertion, w *judge.Work) judge.Verdict {
	switch a.Subject() {
	case assert.SubjectHandshakeMS:
		return judge.Number(a, probe.Millis(h.elapsed))
	case assert.SubjectCertValid:
		valid := 0.0
		if h.valid {
			valid = 1
		}
		return judge.Number(a, valid)
	case assert.SubjectVersion:
		v, ok := versionNumber(h.version)
		if !ok {
			return judge.Absent(a)
		}
		return judge.Number(a, v)
	case assert.SubjectCipher:
		return w.Text(a, tls.CipherSuiteName(h.cipher), true)
	}
	if h.leaf == nil {
		return judge.Failed(errNoCertificate)
	}
	switch a.Subject() {
	case assert.SubjectCertDays:
		return judge.Number(a, h.days())
	case assert.SubjectCertIssuer:
		return w.Text(a, probe.IssuerLabel(h.leaf), true)
	case assert.SubjectCertSubject:
		return w.Text(a, subjectLabel(h.leaf), true)
	case assert.SubjectCertSANs:
		sans, ok := sansText(h.leaf)
		return w.Text(a, sans, ok)
	}
	return judge.Failed(judge.ErrInvalid)
}

// subjectLabel names the leaf's subject as the issuer is named.
func subjectLabel(cert *x509.Certificate) string { return probe.NameLabel(cert.Subject) }

// sansText is the leaf's DNS names, then its IP addresses, in certificate
// order and joined with ", "; false when it has none.
func sansText(cert *x509.Certificate) (string, bool) {
	sans := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		sans = append(sans, ip.String())
	}
	return strings.Join(sans, ", "), len(sans) > 0
}

// versionNumber is a TLS version as a number (TLS 1.2 is 1.2); false for
// any other protocol version.
func versionNumber(v uint16) (float64, bool) {
	switch v {
	case tls.VersionTLS10:
		return 1.0, true
	case tls.VersionTLS11:
		return 1.1, true
	case tls.VersionTLS12:
		return 1.2, true
	case tls.VersionTLS13:
		return 1.3, true
	}
	return 0, false
}

// tlsProbeConfig is the tls probe's client configuration. The chain is
// verified by probeTLS itself, so an invalid one still completes the
// handshake and reports its expiry. TLS 1.0 and 1.1 are accepted: Go's
// client floor of TLS 1.2 would read old LAN appliances (printers, UPS
// cards, switches) as down when the point is to watch their certificate.
// The probe sends nothing over the connection.
func tlsProbeConfig(serverName string) *tls.Config {
	return &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,             //nolint:gosec // verified by probeTLS against the roots and server_name
		MinVersion:         tls.VersionTLS10, //nolint:gosec // old appliances; see the doc comment
	}
}

// Probe is the tls probe, as the registry runs it.
func Probe(ctx context.Context, g *probe.Guard, params map[string]any, address string, _ map[string]string) []probe.Observation {
	return probeTLS(ctx, g, params, address, nil)
}
