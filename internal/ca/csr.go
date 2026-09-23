package ca

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrBadCSR wraps every refusal of a certificate request.
var ErrBadCSR = errors.New("certificate request rejected")

// CheckCSR validates csrPEM without signing it: PEM shape, parseable
// request, valid signature, accepted key type. Enrollment paths run it
// before consuming anything (a rejected request must not burn a token
// use); SignCSR and SignAgentCSR run it again before issuing.
func CheckCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("%w: expected a PEM CERTIFICATE REQUEST", ErrBadCSR)
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCSR, err)
	}
	if err := req.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: signature: %v", ErrBadCSR, err)
	}
	switch k := req.PublicKey.(type) {
	case ed25519.PublicKey:
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() && k.Curve != elliptic.P384() {
			return nil, fmt.Errorf("%w: ECDSA keys must use P-256 or P-384", ErrBadCSR)
		}
	default:
		return nil, fmt.Errorf("%w: key must be Ed25519 or ECDSA P-256/P-384", ErrBadCSR)
	}
	return req, nil
}

// parseCSR is CheckCSR under the internal name the signers call it by.
func parseCSR(csrPEM []byte) (*x509.CertificateRequest, error) { return CheckCSR(csrPEM) }

// SignCSR issues a worker certificate for the key in csrPEM: the CA names
// the worker (CN, org and zone URI SANs).
func (c *CA) SignCSR(csrPEM []byte, orgID, zoneID uuid.UUID, workerName string) ([]byte, *x509.Certificate, error) {
	if c.cert == nil {
		return nil, nil, fmt.Errorf("sign certificate request: %w", ErrNotReady)
	}
	req, err := parseCSR(csrPEM)
	if err != nil {
		return nil, nil, err
	}
	tpl, err := c.leafTemplate(&orgID, zoneID, workerName)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificate(c.rand, tpl, c.cert, req.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign certificate: %w", err)
	}
	crt, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse signed certificate: %w", err)
	}
	return pemCert(der), crt, nil
}

// SignAgentCSR issues an agent certificate for the key in csrPEM: the CA
// names the agent (CN `pulsix-agent <agentName>`, org URI SAN, no zone
// SAN, AgentCertValidity). The agent keeps its private key; only the
// certificate crosses the wire.
func (c *CA) SignAgentCSR(csrPEM []byte, orgID uuid.UUID, agentName string) ([]byte, *x509.Certificate, error) {
	if err := validateAgentName(agentName); err != nil {
		return nil, nil, fmt.Errorf("sign agent certificate request: %w", err)
	}
	if c.cert == nil {
		return nil, nil, fmt.Errorf("sign agent certificate request: %w", ErrNotReady)
	}
	req, err := parseCSR(csrPEM)
	if err != nil {
		return nil, nil, err
	}
	tpl, err := c.agentTemplate(orgID, agentName)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificate(c.rand, tpl, c.cert, req.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign agent certificate: %w", err)
	}
	crt, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse signed certificate: %w", err)
	}
	return pemCert(der), crt, nil
}

// CAPEM is the root certificate as PEM (the chain a worker stores).
func (c *CA) CAPEM() []byte { return pemCert(c.cert.Raw) }

// CAPin is the fingerprint an enrollment token carries: SHA-256 of the
// root certificate's DER.
func (c *CA) CAPin() [32]byte { return sha256.Sum256(c.cert.Raw) }

// ParseLeaf decodes the first PEM certificate of certPEM.
func ParseLeaf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM CERTIFICATE block")
	}
	return x509.ParseCertificate(block.Bytes)
}
