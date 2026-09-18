package worker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// EnsureIdentity makes sure cfg's certificate bundle exists before the
// worker dials. A usable bundle on disk wins and PULSIX_ENROLL_TOKEN is
// ignored (spec 2026-09-18 §4); otherwise the worker enrolls with the
// token: a fresh ECDSA P-256 key that never leaves the host, a CSR, and the
// Enroll call over a connection pinned to the token's CA fingerprint.
func EnsureIdentity(ctx context.Context, cfg Config, log *slog.Logger) error {
	return ensureIdentity(ctx, cfg, log, time.Now, nil)
}

func ensureIdentity(ctx context.Context, cfg Config, log *slog.Logger, now func() time.Time, extra []grpc.DialOption) error {
	if bundleUsable(cfg, now()) {
		if cfg.EnrollToken != "" {
			log.InfoContext(ctx, "a valid certificate bundle exists; "+envEnrollToken+" is ignored", "cert", cfg.CertPath)
		}
		return nil
	}
	if cfg.EnrollToken == "" {
		return fmt.Errorf("no usable certificate bundle at %s and no %s to enroll with", cfg.CertPath, envEnrollToken)
	}
	tok, err := enrolltoken.Parse(cfg.EnrollToken)
	if err != nil {
		return fmt.Errorf("%s: %w", envEnrollToken, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate worker key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return fmt.Errorf("create certificate request: %w", err)
	}
	serverName, err := serverNameOf(cfg)
	if err != nil {
		return err
	}
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(pinnedTLS(tok.CAPin, serverName)))}, extra...)
	conn, err := grpc.NewClient("passthrough:///"+cfg.ManagerAddr, opts...)
	if err != nil {
		return fmt.Errorf("new gRPC client for %s: %w", cfg.ManagerAddr, err)
	}
	defer func() { _ = conn.Close() }()
	resp, err := workerpb.NewWorkerServiceClient(conn).Enroll(ctx, &workerpb.EnrollRequest{
		TokenId: tok.ID.String(), Secret: tok.Secret,
		Csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), Version: cfg.Version,
	})
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	caBlock, _ := pem.Decode(resp.GetCaChain())
	if caBlock == nil || enrolltoken.Pin(caBlock.Bytes) != tok.CAPin {
		return errors.New("the manager returned a CA that does not match the enrollment token")
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode worker key: %w", err)
	}
	// The key first, the certificate last: a crash in between leaves no
	// usable bundle, and the next start enrolls again (with a new token).
	for _, f := range []struct {
		path    string
		content []byte
		mode    os.FileMode
	}{
		{cfg.KeyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600},
		{cfg.CAPath, resp.GetCaChain(), 0o644},
		{cfg.CertPath, resp.GetCertificate(), 0o644},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			return fmt.Errorf("create identity directory: %w", err)
		}
		if err := writeFileAtomic(filepath.Dir(f.path), filepath.Base(f.path), f.content, f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.path, err)
		}
	}
	log.InfoContext(ctx, "worker enrolled", "worker_id", resp.GetWorkerId(), "identity", filepath.Dir(cfg.CertPath))
	return nil
}

// bundleUsable reports whether the configured bundle loads, its key
// matches, the certificate has not expired and the CA file parses.
func bundleUsable(cfg Config, now time.Time) bool {
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil || !now.Before(id.Leaf().NotAfter) {
		return false
	}
	caPEM, err := os.ReadFile(cfg.CAPath)
	if err != nil {
		return false
	}
	return x509.NewCertPool().AppendCertsFromPEM(caPEM)
}
