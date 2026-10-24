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
	"github.com/noixdev/pulsix/internal/atomicfile"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/config"
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

// reuseBundle reports whether dir already holds a valid enrollment and,
// if so, refreshes ca.pem and keeps it. It runs before any zone slug is
// resolved (see RunWorkerAdd) and never generates a CA: without a stored
// CA no bundle can be valid, and generation is left to the normal path,
// which only runs once a zone exists (i.e. once a manager has started).
func reuseBundle(ctx context.Context, store ca.Store, cfg config.Config, dir string, lookup serialLookup, stderr io.Writer) (bool, error) {
	if !bundlePresent(dir) {
		return false, nil
	}
	if _, found, err := store.Load(ctx); err != nil {
		return false, fmt.Errorf("load CA: %w", err)
	} else if !found {
		return false, nil
	}
	caInst, err := openCA(ctx, store, cfg)
	if err != nil {
		return false, err
	}
	reusable, err := bundleReusable(ctx, dir, caInst, lookup)
	if err != nil || !reusable {
		return false, err
	}
	if err := writeFileAtomic(dir, bundleCAFile, caPEMOf(caInst), 0o644); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(stderr, "worker add: the certificate bundle in %s is still valid; reusing it\n", dir)
	return true, nil
}

// bundlePresent reports whether dir has both the certificate and the key.
func bundlePresent(dir string) bool {
	for _, name := range []string{bundleCertFile, bundleKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// enrollBundle issues and registers a new worker certificate for zone z
// and writes the bundle into dir. Key material never reaches stdout or
// stderr.
func enrollBundle(ctx context.Context, z zoneRow, name string, caPEM []byte, dir string,
	reg workerRegistrar, issuer certIssuer, stderr io.Writer,
) error {
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
// pair matches, whose certificate verifies against the instance CA, and
// whose serial is still registered for the zone the certificate names. A
// missing or invalid bundle is simply not reusable; only an unexpected
// registry failure is an error.
func bundleReusable(ctx context.Context, dir string, verifier clientVerifier, lookup serialLookup) (bool, error) {
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
	if err != nil {
		return false, nil
	}
	w, err := lookup.GetBySerial(ctx, identity.Serial)
	if errors.Is(err, workers.ErrNotFound) {
		return false, nil
	}
	if errors.Is(err, workers.ErrRevoked) {
		// Re-enrolling on its own would undo the revocation, and the name
		// is still taken by the revoked row: say how to bring the bundled
		// worker back instead (final review minor 8). The worker container
		// keeps its old certificate in memory, so it is recreated too.
		return false, fmt.Errorf("the worker %q in %s was revoked. To enroll it again, delete it on the Workers page, "+
			"then run: docker compose up -d --force-recreate worker-enroll worker", identity.WorkerName, dir)
	}
	if err != nil {
		return false, fmt.Errorf("look up bundle worker: %w", err)
	}
	return w.ZoneID == identity.ZoneID, nil
}

// writeFileAtomic writes content to dir/name through a temp file and a
// rename, so a reader never sees a half-written file. An identical file is
// left untouched.
func writeFileAtomic(dir, name string, content []byte, mode os.FileMode) error {
	path := filepath.Join(dir, name)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return nil
	}
	// atomicfile.Write since backlog 2.64: this had its own copy, which
	// flushed neither the file nor the directory — a bundle the worker
	// container is about to read deserves both.
	if err := atomicfile.Write(dir, name, content, mode); err != nil {
		return fmt.Errorf("write bundle %s: %w", name, err)
	}
	return nil
}

// Compile-time proof that the production collaborators fit the ports.
var (
	_ clientVerifier = (*ca.CA)(nil)
	_ serialLookup   = (*workers.Service)(nil)
)
