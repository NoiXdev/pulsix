// Worker add, Docker-free: the slug resolution rules and the output shape,
// driven through fakes. The SQL wiring behind RunWorkerAdd is exercised by
// Task 17's end-to-end wiring test (it needs a database container).
package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/workers"
)

// fakeWorkerAdd is a full fake stack for workerAdd.
type fakeWorkerAdd struct {
	zones    []zoneRow
	zonesErr error
	issued   int
	regOrg   uuid.UUID
	regName  string
	regZone  uuid.UUID
}

func (f *fakeWorkerAdd) ZonesBySlug(context.Context, string) ([]zoneRow, error) {
	return f.zones, f.zonesErr
}

func (f *fakeWorkerAdd) Issue(context.Context, *uuid.UUID, uuid.UUID, string) ([]byte, []byte, *big.Int, error) {
	f.issued++
	return []byte("CERT-PEM"), []byte("KEY-PEM"), big.NewInt(42), nil
}

func (f *fakeWorkerAdd) Register(_ context.Context, orgID uuid.UUID, name string, zoneID uuid.UUID, _ *big.Int) error {
	f.regOrg, f.regName, f.regZone = orgID, name, zoneID
	return nil
}

// TestWorkerAddOutputShape: happy path prints cert, key, CA in that order,
// with the usage note last and nothing else.
func TestWorkerAddOutputShape(t *testing.T) {
	zoneID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	f := &fakeWorkerAdd{zones: []zoneRow{{OrgID: orgID, ID: zoneID, OrgSlug: "acme"}}}
	var out bytes.Buffer
	err := workerAdd(context.Background(), f, f, f, []byte("CA-PEM"), "fra-1", "", "fra-1-a", "pulsix-manager", &out)
	if err != nil {
		t.Fatalf("workerAdd: %v", err)
	}
	if f.issued != 1 || f.regName != "fra-1-a" || f.regOrg != orgID || f.regZone != zoneID {
		t.Fatalf("flow registered the wrong worker: issued=%d reg=(%s, %s, %s)", f.issued, f.regOrg, f.regName, f.regZone)
	}
	got := out.String()
	for _, want := range []string{"# client certificate\nCERT-PEM", "# private key\nKEY-PEM", "# CA certificate\nCA-PEM", "PULSIX_MANAGER_ADDR", "PULSIX_WORKER_CERT", "PULSIX_WORKER_KEY", "PULSIX_CA_CERT",
		// The server certificate names the configured server name, not the
		// address the worker dials: without it the handshake fails.
		"PULSIX_MANAGER_SERVER_NAME=pulsix-manager"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if idxCert := strings.Index(got, "# client certificate"); idxCert > strings.Index(got, "# private key") {
		t.Fatalf("certificate must be printed before the key:\n%s", got)
	}
}

// TestResolveZoneErrors: unknown slug, ambiguous slug without --org, and
// --org that matches none.
func TestResolveZoneErrors(t *testing.T) {
	a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ambiguous := []zoneRow{
		{OrgID: a, ID: uuid.Must(uuid.NewV7()), OrgSlug: "acme"},
		{OrgID: b, ID: uuid.Must(uuid.NewV7()), OrgSlug: "other"},
	}
	cases := []struct {
		name    string
		rows    []zoneRow
		orgSlug string
		wantErr string
	}{
		{"unknown", nil, "", `no zone with slug "x"`},
		{"ambiguous", ambiguous, "", "used by 2 organizations"},
		{"wrong org", ambiguous, "nope", `organization "nope" has no zone`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveZone(context.Background(), &fakeWorkerAdd{zones: tc.rows}, "x", tc.orgSlug)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestResolveZoneDisambiguation: --org picks the right organization's zone.
func TestResolveZoneDisambiguation(t *testing.T) {
	a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	rows := []zoneRow{
		{OrgID: a, ID: uuid.Must(uuid.NewV7()), OrgSlug: "acme"},
		{OrgID: b, ID: uuid.Must(uuid.NewV7()), OrgSlug: "other"},
	}
	z, err := resolveZone(context.Background(), &fakeWorkerAdd{zones: rows}, "x", "other")
	if err != nil {
		t.Fatal(err)
	}
	if z.OrgID != b {
		t.Fatalf("picked org %s, want %s", z.OrgID, b)
	}
}

// TestWorkerAddLookupError propagates the source's error wrapped.
func TestWorkerAddLookupError(t *testing.T) {
	f := &fakeWorkerAdd{zonesErr: errors.New("db down")}
	var out bytes.Buffer
	err := workerAdd(context.Background(), f, f, f, nil, "fra-1", "", "fra-1-a", "pulsix-manager", &out)
	if err == nil || !errors.Is(err, f.zonesErr) {
		t.Fatalf("err = %v, want the lookup error wrapped", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a failed enrollment must print nothing, got %q", out.String())
	}
}

// TestBuildWithGRPCListener (integration): with GRPCListen set, Build
// wires the gRPC server; the CA's sealed blob lands in the database, so a
// second Build reuses it — the same reason a manager restart keeps the
// same instance CA.
func TestBuildWithGRPCListener(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.GRPCListen = ":0"

	a, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.GRPC == nil {
		t.Fatal("Build with GRPCListen set must produce a gRPC server")
	}
	if a.GRPCListen != ":0" {
		t.Fatalf("GRPCListen = %q, want :0", a.GRPCListen)
	}

	// A second Build reopens the same sealed CA instead of failing or
	// generating a second root.
	b, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("second Build with the stored CA: %v", err)
	}
	defer b.Close()
	if b.GRPC == nil {
		t.Fatal("second Build must also wire the gRPC server")
	}
}

// TestRunWorkerAdd (integration): the command enrolls a worker against the
// real database — the printed material is a usable key pair chaining to the
// printed CA, and the serial then resolves through the worker registry the
// gRPC interceptor looks serials up by.
func TestRunWorkerAdd(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)

	// Seed one organization with one non-default zone, the way the
	// operator's instance looks when the first worker is enrolled.
	admin, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	q := gen.New(admin.Pool())
	orgID, zoneID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	if _, err := q.CreateOrganization(ctx, gen.CreateOrganizationParams{ID: orgID, Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateZone(ctx, gen.CreateZoneParams{ID: zoneID, OrgID: orgID, Slug: "fra-1", Name: "Frankfurt 1"}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := RunWorkerAdd(ctx, cfg, WorkerAddOptions{ZoneSlug: "fra-1", Name: "fra-1-a"}, &stdout, io.Discard); err != nil {
		t.Fatalf("RunWorkerAdd: %v", err)
	}
	out := stdout.String()
	certPEM, keyPEM, caPEM, err := splitPEMSections(out)
	if err != nil {
		t.Fatalf("output does not carry the PEM blocks: %v\n%s", err, out)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("printed material is not a usable key pair: %v", err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject.CommonName != "pulsix-worker fra-1-a" {
		t.Fatalf("worker CN = %q", parsed.Subject.CommonName)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("printed CA PEM does not parse as a certificate")
	}
	// VerifyOptions's default key usage is ServerAuth; the worker
	// certificate is a client certificate, so say so.
	if _, err := parsed.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("worker certificate does not verify against the printed CA: %v", err)
	}

	// The registry row the gRPC interceptor looks serials up by exists.
	appDB, err := db.OpenAsRole(ctx, cfg.DatabaseURL, "pulsix_app")
	if err != nil {
		t.Fatal(err)
	}
	defer appDB.Close()
	w, err := workers.NewService(workers.NewSQLStore(appDB)).GetBySerial(ctx, parsed.SerialNumber)
	if err != nil {
		t.Fatalf("GetBySerial after worker add: %v", err)
	}
	if w.Name != "fra-1-a" || w.ZoneID != zoneID || w.CertSerial.Cmp(parsed.SerialNumber) != 0 {
		t.Fatalf("worker row = %+v", w)
	}
	if !strings.Contains(out, "PULSIX_MANAGER_ADDR") {
		t.Fatalf("usage note must name the worker's env vars:\n%s", out)
	}
}

// TestRunWorkerAddUnknownZone (integration): an unknown slug fails without
// printing any key material.
func TestRunWorkerAddUnknownZone(t *testing.T) {
	cfg := testConfig(t)
	var stdout bytes.Buffer
	err := RunWorkerAdd(t.Context(), cfg, WorkerAddOptions{ZoneSlug: "no-such-zone", Name: "fra-1-a"}, &stdout, io.Discard)
	if err == nil {
		t.Fatal("unknown zone must fail")
	}
	if stdout.Len() != 0 {
		t.Fatalf("a failed enrollment must print nothing, got:\n%s", stdout.String())
	}
}

// splitPEMSections splits the command's stdout into its three PEM blocks,
// keyed by the section markers the command prints.
func splitPEMSections(out string) (certPEM, keyPEM, caPEM []byte, err error) {
	sections := map[string][]byte{}
	var current string
	for line := range strings.SplitSeq(out, "\n") {
		switch line {
		case "# client certificate", "# private key", "# CA certificate":
			current = line
		default:
			if current != "" && (strings.HasPrefix(line, "-----BEGIN ") || (len(sections[current]) > 0 && line != "")) {
				sections[current] = append(sections[current], []byte(line+"\n")...)
			}
		}
	}
	certPEM, keyPEM, caPEM = sections["# client certificate"], sections["# private key"], sections["# CA certificate"]
	if len(certPEM) == 0 || len(keyPEM) == 0 || len(caPEM) == 0 {
		return nil, nil, nil, errors.New("missing one of the three PEM sections")
	}
	return certPEM, keyPEM, caPEM, nil
}
