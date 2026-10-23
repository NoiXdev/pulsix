// Identity persistence for the agent: the client certificate, its key,
// the CA chain to verify the manager with and a small metadata file, all
// in the data dir (PULSIX_DATA_DIR, default /etc/pulsix). Everything is
// written owner-only through temp-file-and-rename, so a crash never
// leaves a torn identity, and the key is generated ONCE — renewals swap
// the certificate for the same key's new one, exactly like the worker's
// identity machinery this mirrors (internal/worker/identity.go).
//
// "Valid identity" is the loop's skip-enrollment gate: the files parse,
// the certificate is inside its validity window and it carries the org
// URI SAN (urn:pulsix:org:…, the SAN shape the instance CA puts on every
// agent certificate). A broken or expired identity is NOT an error at
// load time — it is simply not valid, and the enrollment path (which
// requires a token) decides what to do; that keeps the recovery paths
// self-healing instead of demanding manual file surgery.
package agent

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// orgSANPrefix is the URI SAN prefix the instance CA puts on agent
// certificates' organization binding (internal/ca's pulsixURI format).
const orgSANPrefix = "urn:pulsix:org:"

// Identity is the agent's loaded identity: certificate + key (presented
// on every TLS handshake) and the host id the enrollment bound it to.
type Identity struct {
	// mu guards the certificate and its parsed leaf, the only fields a
	// renewal changes. The loop replaces them from its own goroutine
	// while the TLS stack reads them from a connection goroutine —
	// DialAgent hands every handshake Certificate() through
	// GetClientCertificate, and the connection the renewal RPC rode is
	// still open when Replace lands. The key, host id and data dir never
	// change after the identity is built, so they need no guard.
	mu     sync.RWMutex
	cert   *tls.Certificate
	leaf   *x509.Certificate
	key    crypto.Signer
	hostID string
	dir    string // the data dir the identity lives in
}

// identityMeta is agent.json: the host the enrollment bound this identity
// to and when the current certificate was issued (informational — the
// certificate's own validity window is authoritative).
type identityMeta struct {
	HostID   string    `json:"host_id"`
	IssuedAt time.Time `json:"issued_at"`
}

// LoadIdentity loads the persisted identity from dir. Files absent, or
// present but unparseable, mean "no identity here" (nil, nil) — the
// enrollment path reuses whatever pieces still load (a surviving key is
// never regenerated); only real I/O errors (a directory that cannot be
// read) propagate.
func LoadIdentity(dir string) (*Identity, error) {
	id, _, err := LoadIdentityDetail(dir)
	return id, err
}

// LoadIdentityDetail is LoadIdentity plus the reason dir holds no usable
// identity — "" when it holds one (backlog 2.103).
//
// LoadIdentity collapses "the files are not there" and "the files are
// there and do not parse" into (nil, nil) deliberately, and that must
// not change: the enrollment path depends on a corrupt identity
// re-enrolling rather than wedging the agent. But a caller that REPORTS
// the outcome needs the difference. Without it the agent logged the
// same line on a first start and on a truncated certificate file, which
// is exactly the question an operator watching an agent re-enrol over
// and over starts with.
//
// The reason is for a human to read, not for a caller to branch on:
// anything that wants to act on the distinction should ask for a
// narrower predicate rather than matching this text.
func LoadIdentityDetail(dir string) (*Identity, string, error) {
	certPEM, err := readOptional(filepath.Join(dir, certFileName))
	if err != nil {
		return nil, "", err
	}
	keyPEM, err := readOptional(filepath.Join(dir, keyFileName))
	if err != nil {
		return nil, "", err
	}
	if certPEM == nil || keyPEM == nil {
		return nil, missingFiles(certPEM == nil, keyPEM == nil), nil
	}
	id, err := identityFromPEM(dir, certPEM, keyPEM)
	if err != nil {
		// Unparseable identity files read as absent: the enrollment path
		// re-enrolls (reusing the key when it still parses) instead of
		// failing the agent on disk corruption.
		return nil, fmt.Sprintf("%s and %s are there but did not parse: %v", certFileName, keyFileName, err), nil
	}
	// The host id rides agent.json; a missing or corrupt meta leaves it
	// empty — the certificate is still usable, the id is bookkeeping.
	if metaBytes, err := readOptional(filepath.Join(dir, metaFileName)); err == nil && metaBytes != nil {
		var meta identityMeta
		if json.Unmarshal(metaBytes, &meta) == nil {
			id.hostID = meta.HostID
		}
	}
	return id, "", nil
}

// missingFiles names which of the two halves is absent. Which one it is
// matters to whoever reads it: a key with no certificate is an
// enrollment that was cut short and will reuse that key, while neither
// present is a first start.
func missingFiles(noCert, noKey bool) string {
	switch {
	case noCert && noKey:
		return fmt.Sprintf("neither %s nor %s is there", certFileName, keyFileName)
	case noCert:
		return fmt.Sprintf("%s is not there (the key is)", certFileName)
	default:
		return fmt.Sprintf("%s is not there (the certificate is)", keyFileName)
	}
}

// SaveEnrollment persists a fresh enrollment, key first: the private key
// (generated once — LoadOrGenerateKey normally has it on disk already and
// the rewrite is the same bytes), then the CA chain (already pin-verified
// by the caller — only the pinned block is ever written), the leaf
// certificate and the host id. Owner-only and atomic throughout, so a
// crash mid-enrollment leaves no usable identity but never an unusable
// one — the next start either reconnects or re-enrolls with a new token.
func SaveEnrollment(dir string, key *ecdsa.PrivateKey, certPEM, caPEM []byte, hostID string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dir, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode agent key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := writeFileAtomic(dir, keyFileName, keyPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", keyFileName, err)
	}
	if err := writeFileAtomic(dir, caFileName, caPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", caFileName, err)
	}
	if err := writeFileAtomic(dir, certFileName, certPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", certFileName, err)
	}
	meta, err := json.Marshal(identityMeta{HostID: hostID, IssuedAt: time.Now()})
	if err != nil {
		return fmt.Errorf("encode identity metadata: %w", err)
	}
	if err := writeFileAtomic(dir, metaFileName, meta, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", metaFileName, err)
	}
	return nil
}

// identityFromPEM parses certPEM/keyPEM into an Identity rooted at dir;
// the pair must match (the certificate must be the key's certificate).
func identityFromPEM(dir string, certPEM, keyPEM []byte) (*Identity, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse agent identity: %w", err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil, fmt.Errorf("parse agent certificate: %w", err)
		}
	}
	return &Identity{cert: &pair, leaf: pair.Leaf, key: pair.PrivateKey.(crypto.Signer), dir: dir}, nil
}

// readOptional reads a file, returning (nil, nil) when it does not exist
// and an error for real I/O problems (permissions, a vanished directory).
func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, nil
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	return nil, fmt.Errorf("read %s: %w", path, err)
}

// Leaf is the current certificate, parsed.
func (i *Identity) Leaf() *x509.Certificate {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.leaf
}

// HostID is the host the enrollment bound this identity to.
func (i *Identity) HostID() string { return i.hostID }

// Certificate is the client certificate handed to every TLS handshake
// (tls.Config.GetClientCertificate), so a renewal takes effect on the
// next connection without restarting the loop.
func (i *Identity) Certificate() *tls.Certificate {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.cert
}

// Signer is the persisted private key (renewal CSRs are signed with it).
func (i *Identity) Signer() crypto.Signer { return i.key }

// Valid reports whether the identity can stand in for enrollment: it
// parses (guaranteed by the loader), is inside its validity window and
// carries the org SAN that makes it an agent certificate.
func (i *Identity) Valid(now time.Time) bool {
	if i == nil {
		return false
	}
	leaf := i.Leaf()
	if leaf == nil {
		return false
	}
	return !now.Before(leaf.NotBefore) && now.Before(leaf.NotAfter) && hasOrgSAN(leaf)
}

// NeedsRenewal reports whether less than a third of the validity remains
// (the two-thirds-lifetime renewal window).
func (i *Identity) NeedsRenewal(now time.Time) bool {
	leaf := i.Leaf()
	return leaf.NotAfter.Sub(now) < leaf.NotAfter.Sub(leaf.NotBefore)/3
}

// Replace swaps the identity's certificate for a renewed one (the same
// key's fresh certificate) and persists certificate and metadata. A
// mismatched or malformed certificate changes nothing and is refused —
// the caller then retries with the still-valid current certificate.
func (i *Identity) Replace(certPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("renewed certificate is not a PEM CERTIFICATE")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse renewed certificate: %w", err)
	}
	pub, eq := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !eq || !pub.Equal(i.key.Public()) {
		return fmt.Errorf("renewed certificate does not match the agent's key")
	}
	dir := i.dir
	next := &tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: i.key, Leaf: leaf}
	meta, err := json.Marshal(identityMeta{HostID: i.hostID, IssuedAt: time.Now()})
	if err != nil {
		return fmt.Errorf("encode identity metadata: %w", err)
	}
	if err := writeFileAtomic(dir, certFileName, certPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", certFileName, err)
	}
	if err := writeFileAtomic(dir, metaFileName, meta, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", metaFileName, err)
	}
	// Only after both files are on disk: an identity whose in-memory
	// certificate is ahead of the persisted one would come back as the
	// older certificate on the next restart.
	i.mu.Lock()
	i.cert, i.leaf = next, leaf
	i.mu.Unlock()
	return nil
}

// hasOrgSAN reports whether leaf carries the org URI SAN.
func hasOrgSAN(leaf *x509.Certificate) bool {
	for _, u := range leaf.URIs {
		if strings.HasPrefix(u.String(), orgSANPrefix) {
			return true
		}
	}
	return false
}
