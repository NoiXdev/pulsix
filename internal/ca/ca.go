// Package ca is the instance's internal certificate authority (spec §14).
// On first start it generates a self-signed ECDSA P-384 root, seals it with
// the instance master key and stores it; afterwards it issues client
// certificates for workers and the server certificate for the gRPC
// listener, and verifies presented client chains against that root. This
// package knows nothing about gRPC: the transport layer feeds the chain the
// TLS handshake accepted into VerifyClient.
package ca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors the transport layer maps to outcomes: ErrBadIdentity
// rejects a presented client certificate, ErrNotReady reports Issue or
// VerifyClient use before Ensure, ErrCASealed marks failures while
// producing or unsealing the stored CA material.
var (
	ErrBadIdentity = errors.New("bad client identity")
	ErrNotReady    = errors.New("CA not initialised; call Ensure first")
	ErrCASealed    = errors.New("sealed CA material unusable")
)

const (
	// workerCNPrefix starts the common name of every issued worker
	// certificate; VerifyClient strips it back into the worker name.
	workerCNPrefix = "pulsix-worker "

	// The urn:pulsix:org:<uuid> and urn:pulsix:zone:<uuid> URI SANs carry
	// the identity a verified worker certificate stands for (spec §14).
	orgURIPrefix  = "urn:pulsix:org:"
	zoneURIPrefix = "urn:pulsix:zone:"

	// The root lives 3650 days.
	rootValidity = 3650 * 24 * time.Hour
)

// LeafValidity is the lifetime of every worker and server certificate
// (spec 2026-09-18 §4: 90 days; the worker renews once a third is left).
const LeafValidity = 90 * 24 * time.Hour

// Config names the CA's common name and, optionally, the host the gRPC
// listener serves the server certificate for.
type Config struct {
	CommonName string
	ListenHost string
}

// Sealer is the slice of crypto.Box the CA needs.
type Sealer interface {
	SealAAD(plain, aad []byte) ([]byte, error)
	OpenAAD(sealed, aad []byte) ([]byte, error)
}

// Store persists the sealed CA blob exactly once per instance: a singleton
// row that Save creates or replaces.
type Store interface {
	Load(ctx context.Context) (sealed []byte, found bool, err error)
	Save(ctx context.Context, sealed []byte) error
}

// ClientIdentity is what a verified worker certificate stands for.
type ClientIdentity struct {
	Serial     *big.Int
	OrgID      *uuid.UUID // nil for instance-wide workers
	ZoneID     uuid.UUID
	WorkerName string
}

// caBlob is the JSON sealed into the store; both fields are PEM.
type caBlob struct {
	KeyPEM  string `json:"key_pem"`
	CertPEM string `json:"cert_pem"`
}

// CA issues and verifies certificates from the instance's internal root.
// Ensure must run once before the other methods.
type CA struct {
	store Store
	box   Sealer
	rand  io.Reader
	now   func() time.Time
	cfg   Config

	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pool *x509.CertPool
}

// Option overrides a CA's constructor-injected dependencies.
type Option func(*CA)

// WithRand replaces the entropy source (crypto/rand.Reader by default).
func WithRand(r io.Reader) Option { return func(c *CA) { c.rand = r } }

// WithClock replaces the clock (time.Now by default).
func WithClock(f func() time.Time) Option { return func(c *CA) { c.now = f } }

// New wires the store, the sealing box and the config; the entropy source
// and clock default to crypto/rand and time.Now and are overridable with
// the With* options.
func New(store Store, box Sealer, cfg Config, opts ...Option) (*CA, error) {
	if cfg.CommonName == "" {
		return nil, errors.New("ca: CommonName must be set")
	}
	c := &CA{store: store, box: box, cfg: cfg, rand: crand.Reader, now: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// aad binds the sealed CA blob to its instance: a blob moved to a CA with a
// different common name no longer opens.
func aad(commonName string) []byte { return []byte("ca:" + commonName) }

// Ensure loads the stored CA or, on first start, generates, seals and
// stores one, and keeps it in memory for Issue, Pool and VerifyClient.
func (c *CA) Ensure(ctx context.Context) error {
	sealed, found, err := c.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("load CA: %w", err)
	}
	var blob caBlob
	if found {
		if blob, err = c.open(sealed); err != nil {
			return err
		}
	} else {
		if blob, err = c.generate(); err != nil {
			return err
		}
		plain, err := json.Marshal(blob)
		if err != nil {
			return fmt.Errorf("%w: encode CA: %v", ErrCASealed, err)
		}
		out, err := c.box.SealAAD(plain, aad(c.cfg.CommonName))
		if err != nil {
			return fmt.Errorf("%w: seal CA: %v", ErrCASealed, err)
		}
		if err := c.store.Save(ctx, out); err != nil {
			return fmt.Errorf("save CA: %w", err)
		}
	}
	return c.adopt(blob)
}

// open unseals the stored blob.
func (c *CA) open(sealed []byte) (caBlob, error) {
	plain, err := c.box.OpenAAD(sealed, aad(c.cfg.CommonName))
	if err != nil {
		return caBlob{}, fmt.Errorf("%w: open sealed CA: %v", ErrCASealed, err)
	}
	var blob caBlob
	if err := json.Unmarshal(plain, &blob); err != nil {
		return caBlob{}, fmt.Errorf("%w: decode sealed CA: %v", ErrCASealed, err)
	}
	return blob, nil
}

// adopt parses the PEM blob into the in-memory key, certificate and pool.
func (c *CA) adopt(blob caBlob) error {
	keyBlock, _ := pem.Decode([]byte(blob.KeyPEM))
	certBlock, _ := pem.Decode([]byte(blob.CertPEM))
	if keyBlock == nil || certBlock == nil {
		return fmt.Errorf("%w: sealed CA has no PEM blocks", ErrCASealed)
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("%w: parse CA key: %v", ErrCASealed, err)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("%w: parse CA certificate: %v", ErrCASealed, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	c.key, c.cert, c.pool = key, cert, pool
	return nil
}

// generate creates the self-signed root: ECDSA P-384, CA key usages, 3650
// days. It returns the PEM blob; Ensure seals and stores it.
func (c *CA) generate() (caBlob, error) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), c.rand)
	if err != nil {
		return caBlob{}, fmt.Errorf("%w: generate CA key: %v", ErrCASealed, err)
	}
	serial, err := newSerial(c.rand)
	if err != nil {
		return caBlob{}, fmt.Errorf("%w: draw CA serial: %v", ErrCASealed, err)
	}
	now := c.now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: c.cfg.CommonName},
		NotBefore:             now,
		NotAfter:              now.Add(rootValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(c.rand, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return caBlob{}, fmt.Errorf("%w: self-sign CA certificate: %v", ErrCASealed, err)
	}
	keyPEM, err := pemKey(key)
	if err != nil {
		return caBlob{}, fmt.Errorf("%w: %v", ErrCASealed, err)
	}
	return caBlob{KeyPEM: keyPEM, CertPEM: string(pemCert(der))}, nil
}

// leafTemplate is the certificate Issue and SignCSR both sign: a worker
// client certificate with the pulsix URI SANs, or (orgID nil, workerName
// empty) the gRPC server certificate.
func (c *CA) leafTemplate(orgID *uuid.UUID, zoneID uuid.UUID, workerName string) (*x509.Certificate, error) {
	leafSerial, err := newSerial(c.rand)
	if err != nil {
		return nil, fmt.Errorf("draw serial: %w", err)
	}
	server := orgID == nil && workerName == ""
	commonName := c.cfg.CommonName
	if !server {
		commonName = workerCNPrefix + workerName
	}
	var uris []*url.URL
	if orgID != nil {
		uris = append(uris, pulsixURI("org", *orgID))
	}
	if !server {
		uris = append(uris, pulsixURI("zone", zoneID))
	}
	now := c.now()
	tpl := &x509.Certificate{
		SerialNumber: leafSerial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: now, NotAfter: now.Add(LeafValidity),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, URIs: uris,
	}
	if server {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		if c.cfg.ListenHost != "" {
			tpl.DNSNames = []string{c.cfg.ListenHost}
		}
	} else {
		tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	return tpl, nil
}

// Issue signs a fresh ECDSA P-384 key with the root: a worker certificate
// for (orgID, zoneID, workerName) with the pulsix-* URI SANs, or — when
// orgID is nil and workerName is empty — the gRPC server certificate with
// ExtKeyUsageServerAuth and the listen host as DNS SAN. It returns the
// certificate and key as PEM and the certificate's serial number.
func (c *CA) Issue(_ context.Context, orgID *uuid.UUID, zoneID uuid.UUID, workerName string) (certPEM, keyPEM []byte, serial *big.Int, err error) {
	if c.cert == nil {
		return nil, nil, nil, fmt.Errorf("issue certificate: %w", ErrNotReady)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P384(), c.rand)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate key: %w", err)
	}
	tpl, err := c.leafTemplate(orgID, zoneID, workerName)
	if err != nil {
		return nil, nil, nil, err
	}
	der, err := x509.CreateCertificate(c.rand, tpl, c.cert, &priv.PublicKey, c.key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("sign certificate: %w", err)
	}
	out, err := pemKey(priv)
	if err != nil {
		return nil, nil, nil, err
	}
	return pemCert(der), []byte(out), tpl.SerialNumber, nil
}

// VerifyClient checks that rawCerts (as crypto/tls hands them to a
// VerifyPeerCertificate callback, leaf first) chain to the root with the
// client-auth key usage, and extracts the identity. Any failure is
// ErrBadIdentity.
func (c *CA) VerifyClient(rawCerts [][]byte) (*ClientIdentity, error) {
	if len(rawCerts) == 0 {
		return nil, fmt.Errorf("%w: no certificate presented", ErrBadIdentity)
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: parse certificate: %v", ErrBadIdentity, err)
	}
	// The root issues leaves directly, so the presented chain needs no
	// intermediates; a forged one would not create a valid path anyway.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     c.pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, fmt.Errorf("%w: verify client certificate: %v", ErrBadIdentity, err)
	}
	return identityFrom(leaf)
}

// identityFrom extracts the identity from a verified leaf: the mandatory
// zone and optional organization URI SANs and the worker name behind the
// "pulsix-worker <name>" common name.
func identityFrom(leaf *x509.Certificate) (*ClientIdentity, error) {
	id := &ClientIdentity{Serial: leaf.SerialNumber}
	zoneSeen := false
	for _, u := range leaf.URIs {
		switch s := u.String(); {
		case strings.HasPrefix(s, orgURIPrefix) && id.OrgID == nil:
			v, err := uuid.Parse(s[len(orgURIPrefix):])
			if err != nil {
				return nil, fmt.Errorf("%w: %s SAN %q: %v", ErrBadIdentity, orgURIPrefix, s, err)
			}
			id.OrgID = &v
		case strings.HasPrefix(s, zoneURIPrefix) && !zoneSeen:
			v, err := uuid.Parse(s[len(zoneURIPrefix):])
			if err != nil {
				return nil, fmt.Errorf("%w: %s SAN %q: %v", ErrBadIdentity, zoneURIPrefix, s, err)
			}
			id.ZoneID, zoneSeen = v, true
		}
	}
	if !zoneSeen {
		return nil, fmt.Errorf("%w: certificate %v carries no %s SAN", ErrBadIdentity, leaf.SerialNumber, zoneURIPrefix)
	}
	name, ok := strings.CutPrefix(leaf.Subject.CommonName, workerCNPrefix)
	if !ok || name == "" {
		return nil, fmt.Errorf("%w: common name %q is not a %s<name>", ErrBadIdentity, leaf.Subject.CommonName, workerCNPrefix)
	}
	id.WorkerName = name
	return id, nil
}

// Pool returns the trust pool for client verification and the root itself,
// both for tls.Config wiring. It returns nils before Ensure.
func (c *CA) Pool() (*x509.CertPool, *x509.Certificate) { return c.pool, c.cert }

// NewSerial draws a uniformly random 63-bit positive serial number. It
// panics if the entropy source fails; in production that source is
// crypto/rand.Reader, which cannot fail.
func NewSerial(rand io.Reader) *big.Int {
	s, err := newSerial(rand)
	if err != nil {
		panic("ca: draw serial number: " + err.Error())
	}
	return s
}

// newSerial is NewSerial's error-returning core; generate and Issue wrap
// the failure like any other crypto failure instead of panicking.
func newSerial(rand io.Reader) (*big.Int, error) {
	return crand.Int(rand, new(big.Int).Lsh(big.NewInt(1), 63))
}

// pulsixURI builds the urn:pulsix:<kind>:<uuid> URI SAN for a uuid.
func pulsixURI(kind string, id uuid.UUID) *url.URL {
	return &url.URL{Scheme: "urn", Opaque: "pulsix:" + kind + ":" + id.String()}
}

// pemKey encodes an ECDSA key as the PEM Ensure stores back and Issue
// returns.
func pemKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

// pemCert wraps DER certificate bytes in a PEM block.
func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
