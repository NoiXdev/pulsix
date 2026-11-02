// The `pulsix-manager worker add` subcommand: enroll a zone worker by
// issuing its client certificate from the instance CA and registering the
// certificate's serial with the worker registry, then printing the PEM
// blocks the operator pastes onto the worker host.
//
// The flow lives on four injected collaborators (zone source, registrar,
// issuer, CA PEM) so the slug resolution, ambiguity rules and output shape
// are testable without a database; RunWorkerAdd wires the SQL-backed
// implementations. This is the operator CLI path — a plain database pool,
// no row-level-security role and no organization scope, because a
// management command runs before any request context exists and must
// resolve the zone slug across every organization (slugs are unique per
// org, not instance-wide); the workers table itself carries no RLS by
// design (see migration 00017).
package app

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/workers"
)

// caCommonName is the instance CA's fixed common name — the AAD the sealed
// CA blob binds to, so it must be identical on every start and in every
// code path that opens the CA (Build, worker add).
const caCommonName = "pulsix-manager"

// zoneRow is one zone match for a slug, with its organization.
type zoneRow struct {
	OrgID   uuid.UUID
	ID      uuid.UUID
	OrgSlug string
}

// zoneSource resolves a zone slug, deliberately across organizations: the
// CLI runs outside any request scope.
type zoneSource interface {
	ZonesBySlug(ctx context.Context, slug string) ([]zoneRow, error)
}

// workerRegistrar registers the issued certificate's serial.
type workerRegistrar interface {
	Register(ctx context.Context, orgID uuid.UUID, name string, zoneID uuid.UUID, serial *big.Int) error
}

// certIssuer issues the worker's client certificate.
type certIssuer interface {
	Issue(ctx context.Context, orgID *uuid.UUID, zoneID uuid.UUID, name string) (certPEM, keyPEM []byte, serial *big.Int, err error)
}

// WorkerAddOptions are `worker add`'s inputs.
type WorkerAddOptions struct {
	ZoneSlug string
	OrgSlug  string // disambiguates a zone slug several organizations use
	Name     string
	// BundleDir, when set, writes the certificate bundle (cert.pem,
	// key.pem, ca.pem) into this directory instead of printing it, and
	// reuses a bundle already there while its certificate still verifies
	// against the instance CA and its serial is still registered — the
	// unattended compose enrollment service reruns on every `up`.
	BundleDir string
	// Wait retries zone resolution while the zone does not exist yet (a
	// fresh instance whose setup wizard has not run), instead of failing.
	Wait bool
	// WaitInterval is Wait's retry period; zero means
	// defaultWorkerAddWaitInterval.
	WaitInterval time.Duration
	// Replace revokes and deletes an active worker that already has the
	// name in the organization, then enrolls as usual (see
	// replacingRegister).
	Replace bool
}

// defaultWorkerAddWaitInterval is how often `worker add --wait` retries a
// zone that does not exist yet.
const defaultWorkerAddWaitInterval = 5 * time.Second

// RunWorkerAdd implements `worker add`: resolve the zone, issue the worker
// certificate, register the serial and print the PEM blocks (or write them
// into opts.BundleDir). The zone slug is resolved across organizations;
// when several organizations use it, opts.OrgSlug disambiguates. stderr is
// for human-facing diagnostics only — stdout carries key material and must
// stay pipeable, which is why nothing here logs.
//
// The zone is resolved before the instance CA is opened: a zone exists
// only once a manager has started (and generated the CA), so an enrollment
// that runs alongside a first manager start never races it to generate a
// second CA.
func RunWorkerAdd(ctx context.Context, cfg config.Config, opts WorkerAddOptions, stdout, stderr io.Writer) error {
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	svc := workers.NewService(workers.NewSQLStore(d))
	// A bundle directory that already holds a valid enrollment is reused
	// before anything is resolved: the zone slug may have become ambiguous
	// since (a second organization with its own "default" zone), but this
	// worker is enrolled already and must keep starting.
	if opts.BundleDir != "" {
		reused, err := reuseBundle(ctx, ca.NewSQLStore(d), cfg, opts.BundleDir, svc, stderr)
		if err != nil || reused {
			return err
		}
	}

	src := genZoneSource{q: gen.New(d.Pool())}
	var z zoneRow
	if opts.Wait {
		interval := opts.WaitInterval
		if interval <= 0 {
			interval = defaultWorkerAddWaitInterval
		}
		z, err = waitForZone(ctx, src, opts.ZoneSlug, opts.OrgSlug, interval, stderr)
	} else {
		z, err = resolveZone(ctx, src, opts.ZoneSlug, opts.OrgSlug)
	}
	if err != nil {
		return err
	}

	caInst, err := openCA(ctx, ca.NewSQLStore(d), cfg)
	if err != nil {
		return err
	}
	caPEM := caPEMOf(caInst)

	var reg workerRegistrar = funcRegister{svc: svc}
	if opts.Replace {
		reg = replacingRegister{reg: reg, registry: svc, stderr: stderr}
	}
	if opts.BundleDir != "" {
		return enrollBundle(ctx, z, opts.Name, caPEM, opts.BundleDir, reg, caInst, stderr)
	}
	return workerAdd(ctx, fixedZone{z}, reg, caInst, caPEM,
		opts.ZoneSlug, opts.OrgSlug, opts.Name, cfg.CAListenHost(), stdout)
}

// openCA opens (or, on a brand-new instance, generates) the instance CA.
func openCA(ctx context.Context, store ca.Store, cfg config.Config) (*ca.CA, error) {
	caInst, err := ca.New(store, crypto.NewBox(cfg.MasterKey), ca.Config{
		CommonName: caCommonName, ListenHost: cfg.CAListenHost(),
	})
	if err != nil {
		return nil, fmt.Errorf("open CA: %w", err)
	}
	if err := caInst.Ensure(ctx); err != nil {
		return nil, fmt.Errorf("ensure CA: %w", err)
	}
	return caInst, nil
}

// caPEMOf encodes the instance CA's certificate.
func caPEMOf(caInst *ca.CA) []byte {
	_, caCert := caInst.Pool()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
}

// fixedZone is a zoneSource that answers with the one zone RunWorkerAdd
// already resolved.
type fixedZone struct{ z zoneRow }

// ZonesBySlug implements zoneSource.
func (f fixedZone) ZonesBySlug(context.Context, string) ([]zoneRow, error) {
	return []zoneRow{f.z}, nil
}

// workerAdd is the enrollment flow proper: resolve → issue → register →
// print. Split from RunWorkerAdd so tests can inject fakes. serverName is
// the name the manager's gRPC server certificate carries, printed in the
// usage note because a worker that dials an address other than that name
// must present it explicitly.
func workerAdd(ctx context.Context, src zoneSource, reg workerRegistrar, issuer certIssuer, caPEM []byte, zoneSlug, orgSlug, name, serverName string, stdout io.Writer) error {
	z, err := resolveZone(ctx, src, zoneSlug, orgSlug)
	if err != nil {
		return err
	}
	certPEM, keyPEM, serial, err := issuer.Issue(ctx, &z.OrgID, z.ID, name)
	if err != nil {
		return fmt.Errorf("issue worker certificate: %w", err)
	}
	if err := reg.Register(ctx, z.OrgID, name, z.ID, serial); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	return writeEnrollmentOutput(stdout, certPEM, keyPEM, caPEM, serverName)
}

// writeEnrollmentOutput prints the three PEM blocks plus a usage note
// naming the env vars the worker binary reads — in exactly this order,
// which the docs pin. The single checked write keeps stdout pipeable and
// turns a broken pipe (the operator's terminal closed mid-paste) into the
// error the CLI reports on stderr.
func writeEnrollmentOutput(stdout io.Writer, certPEM, keyPEM, caPEM []byte, serverName string) error {
	// pem.EncodeToMemory terminates every block with a newline already.
	_, err := fmt.Fprintf(stdout, "# client certificate\n%s# private key\n%s# CA certificate\n%s"+
		"# usage: save each block above to a file (cert.pem, key.pem, ca.pem) and set on the worker host:\n"+
		"#   PULSIX_MANAGER_ADDR (the manager's worker endpoint, e.g. manager.example.test:8444),\n"+
		"#   PULSIX_MANAGER_SERVER_NAME=%s (the name the manager's endpoint certificate carries),\n"+
		"#   PULSIX_WORKER_CERT, PULSIX_WORKER_KEY, PULSIX_CA_CERT (the file paths).\n",
		certPEM, keyPEM, caPEM, serverName)
	if err != nil {
		return fmt.Errorf("write enrollment output: %w", err)
	}
	return nil
}

// errZoneNotFound marks a zone slug that resolves to no zone (yet) — the
// one resolution failure `worker add --wait` retries.
var errZoneNotFound = errors.New("zone not found")

// resolveZone picks the zone behind the slug: exactly one organization on
// the instance (the common first-enrollment case) uses the single match;
// an ambiguous slug without --org is refused rather than guessed.
func resolveZone(ctx context.Context, src zoneSource, zoneSlug, orgSlug string) (zoneRow, error) {
	rows, err := src.ZonesBySlug(ctx, zoneSlug)
	if err != nil {
		return zoneRow{}, fmt.Errorf("look up zone %q: %w", zoneSlug, err)
	}
	switch {
	case len(rows) == 0:
		return zoneRow{}, fmt.Errorf("%w: no zone with slug %q", errZoneNotFound, zoneSlug)
	case len(rows) == 1 || orgSlug == "":
		if len(rows) > 1 {
			return zoneRow{}, fmt.Errorf(
				"zone slug %q is used by %d organizations; pass --org <org-slug> to disambiguate", zoneSlug, len(rows))
		}
		return rows[0], nil
	}
	for _, r := range rows {
		if r.OrgSlug == orgSlug {
			return r, nil
		}
	}
	return zoneRow{}, fmt.Errorf("%w: organization %q has no zone with slug %q", errZoneNotFound, orgSlug, zoneSlug)
}

// genZoneSource adapts the sqlc query (org-unscoped by design — the CLI
// path is the one statement in this codebase that must cross
// organizations) to the zoneSource port.
type genZoneSource struct{ q *gen.Queries }

// ZonesBySlug implements zoneSource.
func (s genZoneSource) ZonesBySlug(ctx context.Context, slug string) ([]zoneRow, error) {
	rows, err := s.q.GetZonesBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	out := make([]zoneRow, len(rows))
	for i, r := range rows {
		out[i] = zoneRow{OrgID: r.OrgID, ID: r.ID, OrgSlug: r.OrgSlug}
	}
	return out, nil
}

// funcRegister adapts workers.Service to the registrar port.
type funcRegister struct{ svc *workers.Service }

// Register implements workerRegistrar.
func (f funcRegister) Register(ctx context.Context, orgID uuid.UUID, name string, zoneID uuid.UUID, serial *big.Int) error {
	_, err := f.svc.Register(ctx, orgID, workers.RegisterInput{ZoneID: zoneID, Name: name, CertSerial: serial})
	return err
}
