package statuspage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"time"

	"github.com/noixdev/pulsix/internal/atomicfile"
)

// The self-signed certificate of a network page without tls_cert and
// tls_key, kept in the data directory (R2 of the 3.132 plan).
const (
	selfSignedCert     = "status-page.crt"
	selfSignedKey      = "status-page.key"
	selfSignedLifetime = 2 * 365 * 24 * time.Hour
	selfSignedRenew    = 30 * 24 * time.Hour
)

// selfSigned loads the data directory's self-signed certificate, or makes
// a new one when it is missing, unreadable, within selfSignedRenew of its
// end, or does not name host. hostname is the machine's name, added as a
// DNS name.
func selfSigned(dir, host, hostname string, now time.Time) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, selfSignedCert), filepath.Join(dir, selfSignedKey)
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && c.Leaf != nil &&
		c.Leaf.NotAfter.Sub(now) > selfSignedRenew && names(c.Leaf, host) {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate the status page key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate the status page certificate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Pulsix agent status page"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(selfSignedLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	addName(tmpl, host)
	addName(tmpl, hostname)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create the status page certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("encode the status page key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// The key first: a certificate on disk without its key is replaced on
	// the next start anyway.
	if err := atomicfile.Write(dir, selfSignedKey, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("store the status page key: %w", err)
	}
	if err := atomicfile.Write(dir, selfSignedCert, certPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("store the status page certificate: %w", err)
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// addName adds host to the certificate as an IP address or a DNS name;
// an empty or unspecified host (every interface) adds nothing.
func addName(c *x509.Certificate, host string) {
	if host == "" {
		return
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.IsUnspecified() {
			c.IPAddresses = append(c.IPAddresses, net.IP(a.AsSlice()))
		}
		return
	}
	if !slices.Contains(c.DNSNames, host) {
		c.DNSNames = append(c.DNSNames, host)
	}
}

// names reports whether leaf already names host (or host names nothing).
func names(leaf *x509.Certificate, host string) bool {
	if host == "" {
		return true
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.IsUnspecified() || slices.ContainsFunc(leaf.IPAddresses, func(ip net.IP) bool { return ip.Equal(net.IP(a.AsSlice())) })
	}
	return slices.Contains(leaf.DNSNames, host)
}

// errNoCert is the given certificate not loading.
var errNoCert = errors.New("the status page certificate or key could not be loaded")

// loadGiven loads tls_cert and tls_key.
func loadGiven(certPath, keyPath string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%w: %v", errNoCert, err)
	}
	return c, nil
}
