package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	tlsDefaultTimeout = 10 * time.Second
	tlsDefaultPort    = 443
)

// probeTLS completes a TLS handshake with address:port without trusting the
// certificate, then verifies the chain itself against roots (nil: the
// system roots) and server_name ({address} replaced), so the leaf's expiry
// is reported for an invalid chain too.
func probeTLS(ctx context.Context, g *probe.Guard, params map[string]any, address string, roots *x509.CertPool) []probe.Observation {
	metrics := []string{"tls.up", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days"}

	host, err := probe.TargetHost(address)
	if err != nil {
		return probe.FailAll(err.Error(), metrics...)
	}
	port, ok := probe.PortParam(params, tlsDefaultPort)
	if !ok {
		return probe.FailAll(probe.ErrBadPort.Error(), metrics...)
	}
	// server_name may carry {address}: it becomes the host's address
	// without IPv6 brackets, the form SNI and verification take.
	serverName := strings.ReplaceAll(probe.StrParam(params, "server_name", ""), probe.AddressPlaceholder, host)
	if serverName == "" {
		serverName = host
	}

	ctx, cancel := probe.Bound(ctx, params, tlsDefaultTimeout)
	defer cancel()

	raw, err := g.Dialer(0).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, probe.ErrNotAllowed) {
			return probe.FailAll(probe.ErrNotAllowed.Error(), metrics...)
		}
		return probe.Down("tls")
	}
	conn := tls.Client(raw, tlsProbeConfig(serverName))
	defer func() { _ = conn.Close() }()

	start := time.Now()
	if err := conn.HandshakeContext(ctx); err != nil {
		return probe.Down("tls")
	}
	obs := []probe.Observation{probe.Value("tls.up", 1), probe.Value("tls.handshake_ms", probe.Millis(time.Since(start)))}

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return append(obs, probe.Value("tls.cert_valid", 0))
	}
	now := time.Now()
	leaf := certs[0]
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	_, verr := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		DNSName:       serverName,
		CurrentTime:   now,
	})
	return append(obs,
		probe.BoolValue("tls.cert_valid", verr == nil),
		probe.Value("tls.cert_expiry_days", leaf.NotAfter.Sub(now).Hours()/24),
	)
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
