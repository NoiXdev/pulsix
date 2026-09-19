// Enroll and RenewCertificate: the worker's certificate lifecycle (spec
// 2026-09-18 §4). Enroll is the one method callable without a client
// certificate; it is rate limited per client address.
package grpcsrv

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/workers"
)

// Enroll is rate limited per client address (spec §4): at most
// enrollLimit calls per enrollWindow, counted in process.
const (
	enrollLimit  = 10
	enrollWindow = time.Minute
)

// Enroll implements workerpb.WorkerServiceServer.Enroll.
func (s *Server) Enroll(ctx context.Context, req *workerpb.EnrollRequest) (*workerpb.EnrollResponse, error) {
	ip := s.peerAddr(ctx)
	if !s.enrollLimit.Allow(ip) {
		return nil, status.Error(codes.ResourceExhausted, "too many enrollment attempts from this address; retry in a minute")
	}
	id, err := uuid.Parse(req.GetTokenId())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "enrollment refused")
	}
	res, err := s.deps.Workers.Enroll(ctx, workers.EnrollInput{TokenID: id, Secret: req.GetSecret(), CSRPEM: req.GetCsr(), Version: req.GetVersion()})
	switch {
	case errors.Is(err, workers.ErrEnrollmentRefused):
		return nil, status.Error(codes.Unauthenticated, "enrollment refused")
	case errors.Is(err, workers.ErrBadCSR):
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	case errors.Is(err, workers.ErrNameTaken):
		return nil, status.Error(codes.AlreadyExists, "a worker with this name already exists in the organization")
	case err != nil:
		// Enroll is anonymous: the store's text (table and constraint
		// names, driver messages) is logged, never sent back.
		slog.ErrorContext(ctx, "enroll worker", "token_id", id.String(), "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}
	s.audit(ctx, SystemEvent{Operation: "worker.enroll", ObjectType: "worker", ObjectID: res.Worker.ID.String(),
		OrgID: res.Worker.OrgID.String(), Summary: "worker " + res.Worker.Name + " enrolled from " + ip.String()})
	return &workerpb.EnrollResponse{Certificate: res.CertPEM, CaChain: res.CAPEM, WorkerId: res.Worker.ID.String()}, nil
}

// RenewCertificate implements workerpb.WorkerServiceServer.RenewCertificate.
func (s *Server) RenewCertificate(ctx context.Context, req *workerpb.RenewCertificateRequest) (*workerpb.RenewCertificateResponse, error) {
	_, w, err := authenticated(ctx)
	if err != nil {
		return nil, err
	}
	certPEM, err := s.deps.Workers.Renew(ctx, w, req.GetCsr())
	if errors.Is(err, workers.ErrBadCSR) {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err != nil {
		slog.ErrorContext(ctx, "renew worker certificate", "worker_id", w.ID.String(), "err", err)
		return nil, status.Error(codes.Internal, "certificate renewal failed")
	}
	s.audit(ctx, SystemEvent{Operation: "worker.renew", ObjectType: "worker", ObjectID: w.ID.String(),
		OrgID: w.OrgID.String(), Summary: "worker " + w.Name + " renewed its certificate"})
	return &workerpb.RenewCertificateResponse{Certificate: certPEM}, nil
}

// audit records a system event; the certificate already exists, so a
// failure is logged rather than undoing it.
func (s *Server) audit(ctx context.Context, ev SystemEvent) {
	if err := s.deps.Audit.Record(ctx, ev); err != nil {
		slog.ErrorContext(ctx, "audit worker certificate event", "operation", ev.Operation, "err", err)
	}
}

// peerAddr is the caller's address through the trusted-proxy rules.
func (s *Server) peerAddr(ctx context.Context) netip.Addr {
	remote := ""
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	var xff []string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		xff = md.Get("x-forwarded-for")
	}
	return s.deps.ClientIP.Resolve(remote, xff)
}
