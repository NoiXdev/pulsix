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
	client    workerpb.WorkerServiceClient
	id        *Identity
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
	log       *slog.Logger
	onRenewed func(ctx context.Context)
}

// RenewerOption overrides a Renewer's optional behavior.
type RenewerOption func(*Renewer)

// WithOnRenewed sets a hook Check runs after it swaps a renewed
// certificate into Identity (whether or not saving it to disk also
// succeeded — see Check). cmd/worker/main.go and the embedded worker use
// it to redial: a connection already established before the renewal
// keeps presenting the certificate it handshook with, so only a fresh
// connection actually starts using the new one (ReconnectingClient).
func WithOnRenewed(fn func(ctx context.Context)) RenewerOption {
	return func(r *Renewer) { r.onRenewed = fn }
}

// NewRenewer builds a renewer; nil now/sleep/log use the wall clock and
// slog.Default.
func NewRenewer(client workerpb.WorkerServiceClient, id *Identity, now func() time.Time, sleep func(ctx context.Context, d time.Duration) error, log *slog.Logger, opts ...RenewerOption) *Renewer {
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
	r := &Renewer{client: client, id: id, now: now, sleep: sleep, log: log}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run checks now and then every RenewCheckInterval until ctx ends. A
// problem is logged and, unless the certificate itself was swapped into
// memory regardless (see Check), retried at the next check; the current
// certificate keeps working until it expires.
func (r *Renewer) Run(ctx context.Context) {
	for {
		if err := r.Check(ctx); err != nil {
			r.log.ErrorContext(ctx, "certificate renewal reported a problem", "err", err)
		}
		if err := r.sleep(ctx, RenewCheckInterval); err != nil {
			return
		}
	}
}

// Check renews when due. It probes that the certificate directory is
// writable BEFORE calling RenewCertificate: a read-only mount (the most
// common deployment mistake — a bundle mounted `:ro`, as this project's
// own docs said to do before this fix) would otherwise mint a fresh,
// server-registered certificate every RenewCheckInterval only to discard
// it, forever, because NeedsRenewal keeps seeing the same stale leaf on
// disk. The probe failing is reported as a clear, actionable error and
// nothing is minted that round.
//
// If the directory IS writable but the write itself still fails (a race,
// a full disk, permissions revoked between the probe and the write),
// Identity.Replace swaps the renewed certificate into memory anyway —
// the worker keeps working on it until its next restart rather than
// silently keep presenting one closer to expiry — and that failure is
// still reported so the operator can fix persistence before the restart
// loses the improvement.
func (r *Renewer) Check(ctx context.Context) error {
	if !r.id.NeedsRenewal(r.now()) {
		return nil
	}
	dir := r.id.certDir()
	if err := probeWritable(dir); err != nil {
		return fmt.Errorf("certificate directory %s is read-only (mount it writable so the worker can renew): %w", dir, err)
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
	before := r.id.Leaf().SerialNumber
	replaceErr := r.id.Replace(resp.GetCertificate())
	if r.id.Leaf().SerialNumber.Cmp(before) != 0 {
		// The in-memory certificate changed, saved to disk or not: move
		// future calls to a fresh connection so it actually reaches the
		// wire (see WithOnRenewed's doc comment).
		r.log.InfoContext(ctx, "certificate renewed", "not_after", r.id.Leaf().NotAfter)
		if r.onRenewed != nil {
			r.onRenewed(ctx)
		}
	}
	if replaceErr != nil {
		return fmt.Errorf("save renewed certificate to disk: %w", replaceErr)
	}
	return nil
}
