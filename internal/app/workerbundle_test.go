// Worker enrollment into a bundle directory (fix round 1): the compose
// stack's one-shot enrollment service writes the worker's certificate
// bundle into a volume the worker container mounts read-only, so the
// long-running worker never holds database or master-key access. It must
// be idempotent across restarts and must wait for the zone on a fresh
// instance whose setup wizard has not run yet.
package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// readBundle reads the three bundle files, failing the test when one is
// missing.
func readBundle(t *testing.T, dir string) (certPEM, keyPEM, caPEM []byte) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("bundle file %s: %v", name, err)
		}
		return b
	}
	return read("cert.pem"), read("key.pem"), read("ca.pem")
}

// seedDefaultZone creates one organization with its "default" zone through
// an owner connection.
func seedDefaultZone(t *testing.T, url string) (orgID, zoneID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	admin, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	orgID, zoneID = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: orgID, Slug: "bundle-" + orgID.String()[24:], Name: "Bundle"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneID, OrgID: orgID, Slug: "default", Name: "Default"}); err != nil {
		t.Fatal(err)
	}
	return orgID, zoneID
}

// workerRows counts registry rows of a zone through an owner connection.
func workerRows(t *testing.T, url string, zoneID uuid.UUID) int {
	t.Helper()
	admin, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var n int
	if err := admin.Pool().QueryRow(context.Background(), `SELECT count(*) FROM workers WHERE zone_id = $1`, zoneID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRunWorkerAddBundleIsIdempotent (integration): the first run enrolls
// and writes a usable bundle (private key 0600, nothing on stdout); a rerun
// — a `docker compose up` after a restart — reuses the bundle and registers
// no second worker; once the registry row is gone (the worker was revoked
// or the database reset) the next run enrolls afresh.
func TestRunWorkerAddBundleIsIdempotent(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	_, zoneID := seedDefaultZone(t, cfg.DatabaseURL)
	dir := t.TempDir()
	opts := WorkerAddOptions{ZoneSlug: "default", Name: "worker-1", BundleDir: dir}

	var stdout, stderr bytes.Buffer
	if err := RunWorkerAdd(ctx, cfg, opts, &stdout, &stderr); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("bundle mode must print no key material to stdout, got:\n%s", stdout.String())
	}
	certPEM, keyPEM, caPEM := readBundle(t, dir)
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("bundle is not a usable key pair: %v", err)
	}
	if len(caPEM) == 0 {
		t.Fatal("bundle carries no CA certificate")
	}
	if st, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key.pem mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
	if n := workerRows(t, cfg.DatabaseURL, zoneID); n != 1 {
		t.Fatalf("worker rows after the first run = %d, want 1", n)
	}

	// Restart: reuse, no second row, byte-identical bundle.
	if err := RunWorkerAdd(ctx, cfg, opts, io.Discard, io.Discard); err != nil {
		t.Fatalf("second run: %v", err)
	}
	cert2, key2, _ := readBundle(t, dir)
	if !bytes.Equal(cert2, certPEM) || !bytes.Equal(key2, keyPEM) {
		t.Fatal("a rerun replaced a still-valid bundle")
	}
	if n := workerRows(t, cfg.DatabaseURL, zoneID); n != 1 {
		t.Fatalf("worker rows after a rerun = %d, want still 1", n)
	}

	// The registry row is gone: the bundle is useless, enroll again.
	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Pool().Exec(ctx, `DELETE FROM workers WHERE zone_id = $1`, zoneID); err != nil {
		t.Fatal(err)
	}
	if err := RunWorkerAdd(ctx, cfg, opts, io.Discard, io.Discard); err != nil {
		t.Fatalf("run after revocation: %v", err)
	}
	cert3, _, _ := readBundle(t, dir)
	if bytes.Equal(cert3, certPEM) {
		t.Fatal("a revoked bundle was reused")
	}
	if n := workerRows(t, cfg.DatabaseURL, zoneID); n != 1 {
		t.Fatalf("worker rows after re-enrollment = %d, want 1", n)
	}
}

// TestRunWorkerAddWaitsForTheZone (integration): with Wait, a zone that
// does not exist yet (a fresh instance before the setup wizard) is retried
// until it appears instead of failing the one-shot service.
func TestRunWorkerAddWaitsForTheZone(t *testing.T) {
	cfg := testConfig(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunWorkerAdd(ctx, cfg, WorkerAddOptions{
			ZoneSlug: "default", Name: "worker-1", BundleDir: dir, Wait: true, WaitInterval: 50 * time.Millisecond,
		}, io.Discard, io.Discard)
	}()
	select {
	case err := <-done:
		t.Fatalf("RunWorkerAdd returned before the zone existed: %v", err)
	case <-time.After(3 * time.Second):
	}
	_, zoneID := seedDefaultZone(t, cfg.DatabaseURL)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWorkerAdd after the zone appeared: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("RunWorkerAdd never noticed the zone")
	}
	readBundle(t, dir)
	if n := workerRows(t, cfg.DatabaseURL, zoneID); n != 1 {
		t.Fatalf("worker rows = %d, want 1", n)
	}
}

// TestWaitForZoneStopsOnAmbiguity: only "no such zone yet" is retried; an
// ambiguous slug is a configuration error that waiting cannot fix.
func TestWaitForZoneStopsOnAmbiguity(t *testing.T) {
	f := &fakeWorkerAdd{zones: []zoneRow{
		{OrgID: uuid.Must(uuid.NewV7()), ID: uuid.Must(uuid.NewV7()), OrgSlug: "a"},
		{OrgID: uuid.Must(uuid.NewV7()), ID: uuid.Must(uuid.NewV7()), OrgSlug: "b"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := waitForZone(ctx, f, "default", "", time.Millisecond, io.Discard); err == nil || ctx.Err() != nil {
		t.Fatalf("waitForZone on an ambiguous slug = %v (ctx %v), want an immediate error", err, ctx.Err())
	}
}

// TestRunWorkerAddReusesBundleBeforeResolvingTheZone (fix round 2): a
// valid bundle is reused before the zone slug is resolved, so the compose
// enrollment service keeps succeeding once a second organization (with its
// own "default" zone) exists and PULSIX_WORKER_ORG is empty — the slug is
// ambiguous by then, but the worker is already enrolled.
func TestRunWorkerAddReusesBundleBeforeResolvingTheZone(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	_, zoneID := seedDefaultZone(t, cfg.DatabaseURL)
	dir := t.TempDir()
	opts := WorkerAddOptions{ZoneSlug: "default", Name: "worker-1", BundleDir: dir, Wait: true, WaitInterval: 50 * time.Millisecond}
	if err := RunWorkerAdd(ctx, cfg, opts, io.Discard, io.Discard); err != nil {
		t.Fatalf("first enrollment: %v", err)
	}
	certPEM, _, _ := readBundle(t, dir)

	// A second organization makes "default" ambiguous.
	seedDefaultZone(t, cfg.DatabaseURL)
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	if err := RunWorkerAdd(runCtx, cfg, opts, io.Discard, &stderr); err != nil {
		t.Fatalf("rerun with a second organization: %v (stderr %q)", err, stderr.String())
	}
	if cert2, _, _ := readBundle(t, dir); !bytes.Equal(cert2, certPEM) {
		t.Fatal("the valid bundle was replaced")
	}
	if n := workerRows(t, cfg.DatabaseURL, zoneID); n != 1 {
		t.Fatalf("worker rows = %d, want 1", n)
	}

	// Without a bundle the ambiguity is a clear, immediate failure (even
	// with --wait) naming the flag that resolves it.
	err := RunWorkerAdd(runCtx, cfg, WorkerAddOptions{ZoneSlug: "default", Name: "worker-2", BundleDir: t.TempDir(), Wait: true, WaitInterval: 50 * time.Millisecond}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--org") || runCtx.Err() != nil {
		t.Fatalf("ambiguous enrollment = %v (ctx %v), want an immediate error naming --org", err, runCtx.Err())
	}
}
