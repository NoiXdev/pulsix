package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	tlsDefaultTimeout = 10 * time.Second
	tlsDefaultPort    = 443
)

// probeTLS completes a TLS handshake with address:port without trusting the
// certificate, then verifies the chain itself against roots (nil: the
// system roots) and server_name ({address} replaced), so the leaf's expiry
// is reported for an invalid chain too.
func probeTLS(ctx context.Context, g *Guard, params map[string]any, address string, roots *x509.CertPool) []Observation {
	metrics := []string{"tls.up", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days"}

	host, err := targetHost(address)
	if err != nil {
		return failAll(err.Error(), metrics...)
	}
	port, ok := portParam(params, tlsDefaultPort)
	if !ok {
		return failAll(errBadPort.Error(), metrics...)
	}
	// server_name may carry {address}: it becomes the host's address
	// without IPv6 brackets, the form SNI and verification take.
	serverName := strings.ReplaceAll(strParam(params, "server_name", ""), addressPlaceholder, host)
	if serverName == "" {
		serverName = host
	}

	ctx, cancel := bound(ctx, params, tlsDefaultTimeout)
	defer cancel()

	raw, err := g.Dialer(0).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, ErrNotAllowed) {
			return failAll(ErrNotAllowed.Error(), metrics...)
		}
		return down("tls")
	}
	conn := tls.Client(raw, tlsProbeConfig(serverName))
	defer func() { _ = conn.Close() }()

	start := time.Now()
	if err := conn.HandshakeContext(ctx); err != nil {
		return down("tls")
	}
	obs := []Observation{value("tls.up", 1), value("tls.handshake_ms", millis(time.Since(start)))}

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return append(obs, value("tls.cert_valid", 0))
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
		boolValue("tls.cert_valid", verr == nil),
		value("tls.cert_expiry_days", leaf.NotAfter.Sub(now).Hours()/24),
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
