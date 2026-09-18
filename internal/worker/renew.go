package worker

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"time"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// RenewCheckInterval is how often the worker checks its certificate
// (spec §4: at start and every 6 h).
const RenewCheckInterval = 6 * time.Hour

// Renewer renews the worker's certificate once less than a third of its
// validity is left, reusing the worker's key.
type Renewer struct {
	client workerpb.WorkerServiceClient
	id     *Identity
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	log    *slog.Logger
}

// NewRenewer builds a renewer; nil now/sleep/log use the wall clock and
// slog.Default.
func NewRenewer(client workerpb.WorkerServiceClient, id *Identity, now func() time.Time, sleep func(ctx context.Context, d time.Duration) error, log *slog.Logger) *Renewer {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Renewer{client: client, id: id, now: now, sleep: sleep, log: log}
}

// Run checks now and then every RenewCheckInterval until ctx ends. A
// failed renewal is logged and retried at the next check; the current
// certificate keeps working until it expires.
func (r *Renewer) Run(ctx context.Context) {
	for {
		if err := r.Check(ctx); err != nil {
			r.log.ErrorContext(ctx, "certificate renewal failed; retrying later", "err", err)
		}
		if err := r.sleep(ctx, RenewCheckInterval); err != nil {
			return
		}
	}
}

// Check renews when due and reports whether it did.
func (r *Renewer) Check(ctx context.Context) error {
	if !r.id.NeedsRenewal(r.now()) {
		return nil
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, r.id.Signer())
	if err != nil {
		return fmt.Errorf("create renewal request: %w", err)
	}
	resp, err := r.client.RenewCertificate(ctx, &workerpb.RenewCertificateRequest{
		Csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}),
	})
	if err != nil {
		return fmt.Errorf("renew certificate: %w", err)
	}
	if err := r.id.Replace(resp.GetCertificate()); err != nil {
		return err
	}
	r.log.InfoContext(ctx, "certificate renewed", "not_after", r.id.Leaf().NotAfter)
	return nil
}
