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

type serverCertSource struct {
	ca   *ca.CA
	now  func() time.Time
	mu   sync.Mutex
	cert *tls.Certificate
	leaf *x509.Certificate
}

func newServerCertSource(authority *ca.CA, now func() time.Time) (*serverCertSource, error) {
	s := &serverCertSource{ca: authority, now: now}
	if err := s.issue(); err != nil {
		return nil, err
	}
	return s, nil
}

// issue replaces the served certificate; callers hold mu (or own s).
func (s *serverCertSource) issue() error {
	certPEM, keyPEM, _, err := s.ca.Issue(context.Background(), nil, uuid.Nil, "")
	if err != nil {
		return fmt.Errorf("issue gRPC server certificate: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("load gRPC server certificate: %w", err)
	}
	_, root := s.ca.Pool()
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
	if s.leaf.NotAfter.Sub(s.now()) < s.leaf.NotAfter.Sub(s.leaf.NotBefore)/3 {
		if err := s.issue(); err != nil {
			slog.Error("re-issue gRPC server certificate; serving the current one", "err", err)
		}
	}
	return s.cert, nil
}
