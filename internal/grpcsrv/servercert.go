// The gRPC listener's own certificate. It is chained with the CA so a
// token-enrolling worker can pin the CA by the fingerprint in its token,
// and it is re-issued in memory once less than a third of its validity is
// left — leaves live 90 days, and a manager may run longer than that.
package grpcsrv

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/ca"
)

// certRetryAfter is how long a FAILED re-issue waits before the next
// handshake tries again (backlog 2.64). Without it every handshake
// retried: one CA issue, which is a database write, and one log line
// per connection — so the moment the database is unwell, a fleet
// reconnecting turns a slow database into a hammered one, and buries
// the reason in a flood of identical lines. The window is short
// because the leaf is only in its last third here: there is time to
// spare, but not a day's worth.
const certRetryAfter = time.Minute

type serverCertSource struct {
	now func() time.Time
	log *slog.Logger
	// issue replaces cert and leaf. A field, not a method call, so a
	// test can make issuing fail without standing up a broken CA.
	issue func() error
	mu    sync.Mutex
	cert  *tls.Certificate
	leaf  *x509.Certificate
	// retryAfter is when a failed re-issue may be attempted again;
	// zero means "no failure outstanding".
	retryAfter time.Time
}

func newServerCertSource(authority *ca.CA, now func() time.Time, log *slog.Logger) (*serverCertSource, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &serverCertSource{now: now, log: log}
	s.issue = func() error { return s.issueFrom(authority) }
	if err := s.issue(); err != nil {
		return nil, err
	}
	return s, nil
}

// issueFrom replaces the served certificate; callers hold mu (or own s).
func (s *serverCertSource) issueFrom(authority *ca.CA) error {
	certPEM, keyPEM, _, err := authority.Issue(context.Background(), nil, uuid.Nil, "")
	if err != nil {
		return fmt.Errorf("issue gRPC server certificate: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("load gRPC server certificate: %w", err)
	}
	_, root := authority.Pool()
	pair.Certificate = append(pair.Certificate, root.Raw)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse gRPC server certificate: %w", err)
	}
	s.cert, s.leaf = &pair, leaf
	return nil
}

// get implements tls.Config.GetCertificate.
func (s *serverCertSource) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	due := s.leaf.NotAfter.Sub(now) < s.leaf.NotAfter.Sub(s.leaf.NotBefore)/3
	if due && !now.Before(s.retryAfter) {
		if err := s.issue(); err != nil {
			// Once per window, not once per handshake.
			s.retryAfter = now.Add(certRetryAfter)
			s.log.Error("re-issue gRPC server certificate; serving the current one",
				"retry_after", certRetryAfter, "expires", s.leaf.NotAfter, "err", err)
		} else {
			s.retryAfter = time.Time{}
		}
	}
	return s.cert, nil
}
