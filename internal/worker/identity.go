package worker

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Identity is the worker's client certificate and key, shared by every
// connection: tls.Config.GetClientCertificate reads it on each handshake,
// so a renewed certificate is used by new connections while open streams
// keep the one they were established with (spec 2026-09-18 §4).
type Identity struct {
	certPath string
	mu       sync.RWMutex
	cert     *tls.Certificate
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
	return &Identity{certPath: certPath, cert: &pair}, nil
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

// Replace installs a renewed certificate for the same key. It is checked
// against the key first — a mismatched or malformed certificate changes
// nothing and returns before either step below runs. It is then written
// atomically over the certificate file (temp file + rename in the same
// directory) and swapped into memory.
//
// The swap happens even when the write fails: a worker that cannot
// persist its renewed certificate right now (a read-only mount) must
// still use it over the wire until its next restart rather than silently
// keep presenting one closer to expiry. The write error is still
// returned so the caller can report it — see Renewer.Check, which also
// probes writability before it ever asks the manager to mint this
// certificate, so this path is the backstop for a probe that raced with
// something making the directory read-only between the check and here.
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
	writeErr := writeFileAtomic(i.certDir(), filepath.Base(i.certPath), certPEM, 0o644)
	next := &tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: cur.PrivateKey, Leaf: leaf}
	i.mu.Lock()
	i.cert = next
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
// same directory and a rename, so a reader never sees a torn file.
func writeFileAtomic(dir, name string, content []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
