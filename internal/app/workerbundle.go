// `worker add --bundle-dir`: the unattended enrollment the compose stack
// runs as a one-shot service. It holds database and master-key access only
// for the moment it enrolls, writes the worker's certificate bundle into a
// directory (a volume the worker container mounts read-only) and exits —
// so the long-running worker that executes operator playbooks never sees
// the manager's secrets. It reruns on every `docker compose up`, which is
// why a still-valid bundle is reused instead of registering a new worker.
package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/workers"
)

// The bundle's file names, which the compose worker service's
// PULSIX_WORKER_CERT / PULSIX_WORKER_KEY / PULSIX_CA_CERT point at.
const (
	bundleCertFile = "cert.pem"
	bundleKeyFile  = "key.pem"
	bundleCAFile   = "ca.pem"
)

// clientVerifier checks a presented worker certificate against the
// instance CA (ca.CA implements it).
type clientVerifier interface {
	VerifyClient(rawCerts [][]byte) (*ca.ClientIdentity, error)
}

// serialLookup resolves a certificate serial to its registry row
// (workers.Service implements it).
type serialLookup interface {
	GetBySerial(ctx context.Context, serial *big.Int) (workers.Worker, error)
}

// waitForZone resolves the zone, retrying every interval while it does not
// exist yet. Any other failure — an ambiguous slug, a database error — is
// returned at once: waiting cannot fix it. The first miss prints one line
// to stderr so the operator sees what the service is waiting for.
func waitForZone(ctx context.Context, src zoneSource, zoneSlug, orgSlug string, interval time.Duration, stderr io.Writer) (zoneRow, error) {
	told := false
	for {
		z, err := resolveZone(ctx, src, zoneSlug, orgSlug)
		if err == nil || !errors.Is(err, errZoneNotFound) {
			return z, err
		}
		if !told {
			_, _ = fmt.Fprintf(stderr, "worker add: %v — waiting for it to exist (complete the setup wizard first) …\n", err)
			told = true
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return zoneRow{}, fmt.Errorf("wait for zone %q: %w", zoneSlug, ctx.Err())
		case <-t.C:
		}
	}
}

// enrollBundle reuses the bundle in dir when it is still valid for zone z,
// and otherwise issues and registers a new worker certificate and writes
// the bundle. ca.pem is refreshed whenever it differs from the instance
// CA. Key material never reaches stdout or stderr.
func enrollBundle(ctx context.Context, z zoneRow, name string, caPEM []byte, dir string,
	verifier clientVerifier, lookup serialLookup, reg workerRegistrar, issuer certIssuer, stderr io.Writer,
) error {
	reusable, err := bundleReusable(ctx, dir, z, verifier, lookup)
	if err != nil {
		return err
	}
	if reusable {
		if err := writeFileAtomic(dir, bundleCAFile, caPEM, 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "worker add: the certificate bundle in %s is still valid; reusing it\n", dir)
		return nil
	}
	certPEM, keyPEM, serial, err := issuer.Issue(ctx, &z.OrgID, z.ID, name)
	if err != nil {
		return fmt.Errorf("issue worker certificate: %w", err)
	}
	if err := reg.Register(ctx, z.OrgID, name, z.ID, serial); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	// The certificate lands last: a worker waiting for the bundle starts
	// once cert.pem appears, and by then the key and CA are in place.
	for _, f := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{bundleKeyFile, keyPEM, 0o600},
		{bundleCAFile, caPEM, 0o644},
		{bundleCertFile, certPEM, 0o644},
	} {
		if err := writeFileAtomic(dir, f.name, f.content, f.mode); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(stderr, "worker add: enrolled worker %q (serial %s) and wrote its certificate bundle to %s\n", name, serial, dir)
	return nil
}

// bundleReusable reports whether dir holds a complete bundle whose key
// pair matches, whose certificate verifies against the instance CA for
// zone z, and whose serial is still registered. A missing or invalid
// bundle is simply not reusable; only an unexpected registry failure is an
// error.
func bundleReusable(ctx context.Context, dir string, z zoneRow, verifier clientVerifier, lookup serialLookup) (bool, error) {
	certPEM, errCert := os.ReadFile(filepath.Join(dir, bundleCertFile))
	keyPEM, errKey := os.ReadFile(filepath.Join(dir, bundleKeyFile))
	if errCert != nil || errKey != nil {
		return false, nil
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return false, nil
	}
	identity, err := verifier.VerifyClient(pair.Certificate)
	if err != nil || identity.ZoneID != z.ID {
		return false, nil
	}
	w, err := lookup.GetBySerial(ctx, identity.Serial)
	if errors.Is(err, workers.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up bundle worker: %w", err)
	}
	return w.ZoneID == z.ID, nil
}

// writeFileAtomic writes content to dir/name through a temp file and a
// rename, so a reader never sees a half-written file. An identical file is
// left untouched.
func writeFileAtomic(dir, name string, content []byte, mode os.FileMode) error {
	path := filepath.Join(dir, name)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("write bundle %s: %w", name, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write bundle %s: %w", name, err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write bundle %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write bundle %s: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write bundle %s: %w", name, err)
	}
	return nil
}

// Compile-time proof that the production collaborators fit the ports.
var (
	_ clientVerifier = (*ca.CA)(nil)
	_ serialLookup   = (*workers.Service)(nil)
)
