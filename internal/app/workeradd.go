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
	"fmt"
	"io"
	"math/big"

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

// RunWorkerAdd implements `worker add`: resolve the zone, issue the worker
// certificate, register the serial and print the PEM blocks. The zone slug
// is resolved across organizations; when several organizations use it,
// orgSlug disambiguates. stderr is for human-facing diagnostics only —
// stdout carries key material and must stay pipeable, which is why nothing
// here logs.
func RunWorkerAdd(ctx context.Context, cfg config.Config, zoneSlug, orgSlug, name string, stdout io.Writer) error {
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	caInst, err := ca.New(ca.NewSQLStore(d), crypto.NewBox(cfg.MasterKey), ca.Config{
		CommonName: caCommonName, ListenHost: cfg.GRPCHost(),
	})
	if err != nil {
		return fmt.Errorf("open CA: %w", err)
	}
	if err := caInst.Ensure(ctx); err != nil {
		return fmt.Errorf("ensure CA: %w", err)
	}
	_, caCert := caInst.Pool()

	return workerAdd(ctx,
		genZoneSource{q: gen.New(d.Pool())},
		funcRegister{svc: workers.NewService(workers.NewSQLStore(d))},
		caInst,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}),
		zoneSlug, orgSlug, name, stdout,
	)
}

// workerAdd is the enrollment flow proper: resolve → issue → register →
// print. Split from RunWorkerAdd so tests can inject fakes.
func workerAdd(ctx context.Context, src zoneSource, reg workerRegistrar, issuer certIssuer, caPEM []byte, zoneSlug, orgSlug, name string, stdout io.Writer) error {
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
	return writeEnrollmentOutput(stdout, certPEM, keyPEM, caPEM)
}

// writeEnrollmentOutput prints the three PEM blocks plus a usage note
// naming the env vars the worker binary reads — in exactly this order,
// which the docs pin. The single checked write keeps stdout pipeable and
// turns a broken pipe (the operator's terminal closed mid-paste) into the
// error the CLI reports on stderr.
func writeEnrollmentOutput(stdout io.Writer, certPEM, keyPEM, caPEM []byte) error {
	// pem.EncodeToMemory terminates every block with a newline already.
	_, err := fmt.Fprintf(stdout, "# client certificate\n%s# private key\n%s# CA certificate\n%s"+
		"# usage: save each block above to a file (cert.pem, key.pem, ca.pem) and set on the worker host:\n"+
		"#   PULSIX_MANAGER_ADDR (the manager's gRPC endpoint, e.g. manager.example.test:8444),\n"+
		"#   PULSIX_WORKER_CERT, PULSIX_WORKER_KEY, PULSIX_CA_CERT (the file paths).\n",
		certPEM, keyPEM, caPEM)
	if err != nil {
		return fmt.Errorf("write enrollment output: %w", err)
	}
	return nil
}

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
		return zoneRow{}, fmt.Errorf("no zone with slug %q", zoneSlug)
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
	return zoneRow{}, fmt.Errorf("organization %q has no zone with slug %q", orgSlug, zoneSlug)
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
