package worker

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/noixdev/pulsix/internal/atomicfile"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Identity is the worker's client certificate and key, shared by every
// connection: tls.Config.GetClientCertificate reads it on each handshake,
// so a renewed certificate is used by new connections while open streams
// keep the one they were established with (spec 2026-09-18 §4).
//
// The in-memory certificate (cert) and the persisted one (diskLeaf) are
// tracked separately and can disagree: a renewal always swaps cert, but
// when the identity directory cannot be written, the write is left
// pending (pending holds the bytes still waiting to be saved) and
// diskLeaf keeps describing whatever is actually still on disk. This is
// what lets Renewer.Check report the file's TRUE expiry, not the
// in-memory certificate's much-later one, and retry the write on every
// later check until it succeeds — see PersistPending.
type Identity struct {
	certPath string
	keyPath  string
	mu       sync.RWMutex
	cert     *tls.Certificate
	diskLeaf *x509.Certificate
	pending  []byte // certPEM not yet saved to disk, or nil
}

// LoadIdentity reads the certificate and key files.
func LoadIdentity(certPath, keyPath string) (*Identity, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load worker certificate (%s, %s): %w", certPath, keyPath, err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil, fmt.Errorf("parse worker certificate %s: %w", certPath, err)
		}
	}
	return &Identity{certPath: certPath, keyPath: keyPath, cert: &pair, diskLeaf: pair.Leaf}, nil
}

// Certificate is the current client certificate.
func (i *Identity) Certificate() *tls.Certificate {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.cert
}

// Leaf is the current certificate, parsed.
func (i *Identity) Leaf() *x509.Certificate { return i.Certificate().Leaf }

// Signer is the worker's private key (renewal CSRs are signed with it).
func (i *Identity) Signer() crypto.Signer {
	s, _ := i.Certificate().PrivateKey.(crypto.Signer)
	return s
}

// NeedsRenewal reports whether less than a third of the validity is left.
func (i *Identity) NeedsRenewal(now time.Time) bool {
	leaf := i.Leaf()
	return leaf.NotAfter.Sub(now) < leaf.NotAfter.Sub(leaf.NotBefore)/3
}

// certDir is the directory holding the certificate file (and, by
// convention, key.pem and ca.pem next to it).
func (i *Identity) certDir() string { return filepath.Dir(i.certPath) }

// DiskExpiry is the expiry of the certificate actually persisted on
// disk right now — tracked independently of the in-memory one (Leaf) so
// a renewal that could only be kept in memory never misreports how much
// time is really left before a restart would find an expired file and
// fail to start.
func (i *Identity) DiskExpiry() time.Time {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.diskLeaf.NotAfter
}

// HasPendingWrite reports whether the in-memory certificate has not yet
// been saved to disk (Renewer.Check retries it, and reports the same
// actionable error, on every check until it has).
func (i *Identity) HasPendingWrite() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.pending != nil
}

// Replace installs a renewed certificate for the same key. It is checked
// against the key first — a mismatched or malformed certificate changes
// nothing and returns before either step below runs. It is then swapped
// into memory (Leaf/Certificate) unconditionally and an attempt is made
// to persist it (see persist): a worker that cannot write its identity
// directory right now (a read-only mount) must still use the renewed
// certificate over the wire rather than silently keep presenting one
// closer to expiry, but it must not lose the write forever either —
// persist leaves it as HasPendingWrite when it fails, and Renewer.Check
// retries it on every later check, not just once.
func (i *Identity) Replace(certPEM []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("renewed certificate is not a PEM CERTIFICATE")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse renewed certificate: %w", err)
	}
	cur := i.Certificate()
	signer, ok := cur.PrivateKey.(crypto.Signer)
	pub, eq := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !eq || !pub.Equal(signer.Public()) {
		return errors.New("renewed certificate does not match the worker's key")
	}
	next := &tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: cur.PrivateKey, Leaf: leaf}
	i.mu.Lock()
	i.cert = next
	i.mu.Unlock()
	return i.persist(certPEM, leaf)
}

// PersistPending retries writing the certificate a previous Replace (or
// PersistPending itself) could not save — the exact same bytes, since
// the key never changes and so the certificate never needs re-minting,
// only the write needs retrying. A no-op returning nil when nothing is
// pending.
func (i *Identity) PersistPending() error {
	i.mu.RLock()
	pending := i.pending
	i.mu.RUnlock()
	if pending == nil {
		return nil
	}
	block, _ := pem.Decode(pending) // always well-formed: only Replace's own validated bytes are ever queued
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse pending certificate: %w", err)
	}
	return i.persist(pending, leaf)
}

// persist writes certPEM (whose parsed leaf is passed in, so the caller
// never parses it twice) to the certificate file. On success it becomes
// the tracked disk state and any pending write is cleared; on failure it
// is recorded as the pending write for PersistPending to retry later.
func (i *Identity) persist(certPEM []byte, leaf *x509.Certificate) error {
	writeErr := writeFileAtomic(i.certDir(), filepath.Base(i.certPath), certPEM, 0o644)
	i.mu.Lock()
	if writeErr == nil {
		i.diskLeaf = leaf
		i.pending = nil
	} else {
		i.pending = certPEM
	}
	i.mu.Unlock()
	if writeErr != nil {
		return fmt.Errorf("write renewed certificate: %w", writeErr)
	}
	return nil
}

// probeWritable checks that dir accepts a create-and-remove of a
// temporary file, so a caller can refuse to spend a round trip (or, for
// renewal, mint and immediately orphan a server-registered certificate)
// on a write it already knows will fail.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".writable-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// writeFileAtomic writes content to dir/name through a temp file in the
// same directory and a rename, so a reader never sees a torn file. It
// is atomicfile.Write — this had its own copy, which flushed the file
// but not the directory the rename landed in (backlog 2.64).
func writeFileAtomic(dir, name string, content []byte, mode os.FileMode) error {
	return atomicfile.Write(dir, name, content, mode)
}
