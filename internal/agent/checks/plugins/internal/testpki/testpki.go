// Package testpki makes throwaway certificate authorities and certificates
// for the database plugins' tests: a CA, server certificates for given
// names and client certificates, as PEM. Only tests import it.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// CA is a certificate authority that signs the certificates of one test.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// PEM is the CA certificate, the content of a CA file.
	PEM []byte
}

// Pair is an issued certificate with its key.
type Pair struct {
	// CertPEM is the certificate followed by nothing else.
	CertPEM []byte
	// KeyPEM is the key as an unencrypted PKCS#8 "PRIVATE KEY" block.
	KeyPEM []byte
	// Key is the private key, for tests that encode it another way.
	Key *ecdsa.PrivateKey
}

var serial atomic.Int64

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func template(cn string) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
}

// NewCA returns a fresh self-signed CA.
func NewCA(t testing.TB) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := template("pulsix test CA")
	tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
	tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &CA{cert: cert, key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *CA) issue(t testing.TB, tmpl *x509.Certificate) Pair {
	t.Helper()
	key := newKey(t)
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return Pair{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8}),
		Key:     key,
	}
}

// Server issues a server certificate valid for names: IP addresses go into
// the IP SANs, anything else into the DNS SANs. The first name is the
// common name.
func (ca *CA) Server(t testing.TB, names ...string) Pair {
	t.Helper()
	tmpl := template(names[0])
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	return ca.issue(t, tmpl)
}

// Client issues a client certificate with common name cn (the user name a
// server maps a certificate to).
func (ca *CA) Client(t testing.TB, cn string) Pair {
	t.Helper()
	tmpl := template(cn)
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	return ca.issue(t, tmpl)
}

// Pool is the CA as a certificate pool.
func (ca *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// TLS returns p as a tls.Certificate.
func (p Pair) TLS(t testing.TB) tls.Certificate {
	t.Helper()
	c, err := tls.X509KeyPair(p.CertPEM, p.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Write writes data to dir/name with mode and returns the path. The mode is
// set explicitly, so the umask does not change it.
func Write(t testing.TB, dir, name string, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
