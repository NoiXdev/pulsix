// The agent's CSR builder: an ECDSA P-256 certificate request the manager
// signs into the agent's first client certificate. The key never leaves
// the host; the manager ignores the subject (the token decides the
// certificate's name) but the common name is still set —
// "pulsix-agent <name>" — so a leaked CSR identifies what it was for.
//
// The key is generated ONCE, at the agent's first start, and reused for
// every renewal: renewals are certificate-authenticated swaps of the same
// key's certificate, so the persisted key is the identity's constant.
package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// GenerateKey draws a fresh ECDSA P-256 key.
func GenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// MakeCSR signs a certificate request for key carrying the common name
// "pulsix-agent <name>" and returns it as PEM "CERTIFICATE REQUEST" —
// the exact shape ca.CheckCSR validates and SignAgentCSR signs.
func MakeCSR(key *ecdsa.PrivateKey, name string) ([]byte, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "pulsix-agent " + name},
	}, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate request: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// LoadOrGenerateKey returns the private key PEM at path, generating and
// persisting one (owner-only, atomic) when the file does not exist yet.
// A present-but-unparsable file is an error, never a silent regeneration:
// overwriting an unparseable key could orphan a certificate the manager
// still trusts for this host.
func LoadOrGenerateKey(path string) (*ecdsa.PrivateKey, error) {
	if pemBytes, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			return nil, fmt.Errorf("parse agent key %s: no PEM block", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse agent key %s: %w", path, err)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read agent key %s: %w", path, err)
	}
	key, err := GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate agent key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode agent key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := writeFileAtomic(filepath.Dir(path), filepath.Base(path), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("persist agent key %s: %w", path, err)
	}
	return key, nil
}
