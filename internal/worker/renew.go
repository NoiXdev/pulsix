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

// identityDirRuntimeUID is the worker container's non-root user (see
// deploy/worker/Dockerfile); named here so the actionable log message in
// Check can tell an operator exactly what to chown.
const identityDirRuntimeUID = "65532"

// Check retries any pending write and then renews when due — both
// unconditionally: renewal always asks the manager for a fresh
// certificate and always swaps it into Identity, even when the
// certificate directory turns out to be read-only. A worker whose
// identity directory it cannot write to must still keep working (a
// read-only bundle mount is the most common deployment mistake, and
// refusing to renew at all means it silently dies at its certificate's
// own expiry, restart or not); reusing the same key means only the
// certificate itself needs to move, and it is safe to keep entirely in
// memory. Renewal is still driven off the in-memory certificate
// (NeedsRenewal reads Identity.Leaf), so once a renewal succeeds in
// memory it is not attempted again for another ~60 days — there is no
// re-mint-every-6h loop — regardless of whether the disk write keeps
// failing.
//
// A pending write, though, IS retried on every single Check — not just
// once around the renewal — because an operator who fixes the directory
// days or weeks after the renewal must not have to wait for the NEXT
// renewal (~60 days later) before the certificate is ever saved: without
// this, a restart any time between the on-disk certificate's own expiry
// and that next renewal would find nothing usable on disk and fail to
// start. Each retry that still fails logs the same actionable ERROR: the
// directory, the uid that needs write access, when the certificate ON
// DISK — read from Identity.DiskExpiry, never the in-memory one, which by
// then can be a completely different, much-later-expiring certificate —
// actually expires, and what happens if nothing is done before then. This
// runs on every check while the directory stays broken, including the
// very first one at startup (Run calls Check immediately), so the warning
// appears well before day 90, not just once silently in the log history.
func (r *Renewer) Check(ctx context.Context) error {
	r.retryPendingWrite(ctx) // logs its own actionable error; nothing to propagate
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
	before := r.id.Leaf().SerialNumber
	writeErr := r.id.Replace(resp.GetCertificate())
	if r.id.Leaf().SerialNumber.Cmp(before) == 0 {
		// Replace refused the certificate outright (malformed, or does not
		// match the key) — the manager's fault, not a persistence problem;
		// nothing changed, so there is nothing to reconnect for either.
		return fmt.Errorf("save renewed certificate: %w", writeErr)
	}
	r.log.InfoContext(ctx, "certificate renewed", "not_after", r.id.Leaf().NotAfter)
	if r.onRenewed != nil {
		r.onRenewed(ctx)
	}
	if writeErr != nil {
		r.logPendingWrite(ctx, writeErr)
	}
	return nil
}

// retryPendingWrite retries a certificate left unsaved by an earlier
// Check, logging the same actionable warning again if it still cannot be
// saved (or a one-line success once it finally can). A no-op when
// nothing is pending.
func (r *Renewer) retryPendingWrite(ctx context.Context) {
	if !r.id.HasPendingWrite() {
		return
	}
	if err := r.id.PersistPending(); err != nil {
		r.logPendingWrite(ctx, err)
		return
	}
	r.log.InfoContext(ctx, "previously pending certificate saved to disk")
}

// logPendingWrite is the actionable ERROR for a certificate that exists
// only in memory: the directory, the uid that needs write access, the
// TRUE on-disk expiry and the consequence of a restart before it is
// fixed — which differs once the on-disk certificate has expired: a
// restart then fails at once instead of at that expiry.
func (r *Renewer) logPendingWrite(ctx context.Context, writeErr error) {
	diskExpiry := r.id.DiskExpiry()
	consequence := "if the worker restarts before the directory is made writable, it will stop working once that certificate expires"
	if !r.now().Before(diskExpiry) {
		consequence = "the certificate on disk has already expired: a restart now fails immediately and the worker then has to be enrolled again; " +
			"make the directory writable and wait for the next check (at most 6 h) to save the renewed certificate before restarting"
	}
	r.log.ErrorContext(ctx, "a renewed certificate is only in memory; it could not be saved to disk",
		"dir", r.id.certDir(),
		"fix", "uid "+identityDirRuntimeUID+" needs write access to this directory",
		"on_disk_certificate_expires_at", diskExpiry.Format(time.RFC3339),
		"consequence", consequence,
		"err", writeErr)
}
