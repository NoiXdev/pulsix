package worker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/noixdev/pulsix/internal/enrolltoken"
)

// pinnedTLS is the TLS configuration of the enrollment call: no system
// roots and no trust on first use — the manager's chain must lead to the
// CA whose SHA-256 the enrollment token carries (spec §4).
func pinnedTLS(pin [32]byte, serverName string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: serverName,
		// Verification is verifyPinned below; the standard verifier would
		// consult roots the worker must not trust here.
		InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyPeerCertificate against the pinned CA
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyPinned(raw, pin, serverName)
		},
	}
}

// verifyPinned requires the presented chain to contain the pinned CA and
// the leaf to verify against it for serverName.
func verifyPinned(raw [][]byte, pin [32]byte, serverName string) error {
	if len(raw) < 2 {
		return errors.New("the manager presented no CA certificate to pin against")
	}
	var root *x509.Certificate
	for _, der := range raw[1:] {
		if enrolltoken.Pin(der) == pin {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				return fmt.Errorf("parse pinned CA: %w", err)
			}
			root = c
		}
	}
	if root == nil {
		return errors.New("the manager's CA does not match the fingerprint in the enrollment token")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return fmt.Errorf("parse server certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: serverName, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("verify server certificate against the pinned CA: %w", err)
	}
	return nil
}
