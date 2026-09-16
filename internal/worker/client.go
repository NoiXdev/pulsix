// The worker's mTLS client: one TLS 1.3 connection to the manager's gRPC
// endpoint, authenticated on both ends by certificates the instance CA
// issued (spec §14). The client pins the server by the CA pool it was
// enrolled with and presents the certificate `pulsix-manager worker add`
// issued for it.
package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/noixdev/pulsix/internal/pb/workerpb"
)

// Dial opens the manager's gRPC endpoint with the credentials cfg names:
// the worker's client certificate (CertPath/KeyPath), the CA pool (CAPath)
// the server certificate must verify against, and TLS 1.3 minimum. The
// connection is lazy — gRPC establishes it at the first RPC and keeps
// re-establishing it, so a manager restart heals without the loop's help;
// bad TLS material on the other hand fails right here.
func Dial(ctx context.Context, cfg Config) (*grpc.ClientConn, error) {
	return dial(ctx, cfg, nil)
}

// dial builds the connection; extra dial options exist for the bufconn
// test harness. The TLS ServerName is cfg.ServerName when set, otherwise
// the host part of ManagerAddr (see Config.ServerName's own doc comment
// for why the two can legitimately differ).
func dial(ctx context.Context, cfg Config, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	tlsCfg, err := loadTLS(cfg)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(cfg.ManagerAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve server name from %q: %w", cfg.ManagerAddr, err)
	}
	tlsCfg.ServerName = host
	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}

	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		// The same explicit limits the manager's server sets
		// (workerpb/limits.go): a claim embeds the inventory snapshot.
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(workerpb.MaxManagerToWorkerBytes),
			grpc.MaxCallSendMsgSize(workerpb.MaxWorkerToManagerBytes),
		),
	}, extra...)
	conn, err := grpc.NewClient("passthrough:///"+cfg.ManagerAddr, opts...)
	if err != nil {
		return nil, fmt.Errorf("new gRPC client for %s: %w", cfg.ManagerAddr, err)
	}
	return conn, nil
}

// loadTLS assembles the mutual-TLS configuration from the configured
// files: the client key pair and the CA pool for server verification.
// MinVersion pins TLS 1.3 — the spec's transport floor.
func loadTLS(cfg Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load worker certificate (%s, %s): %w", cfg.CertPath, cfg.KeyPath, err)
	}
	caPEM, err := os.ReadFile(cfg.CAPath)
	if err != nil {
		return nil, fmt.Errorf("read CA pool file %s: %w", cfg.CAPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA pool file %s contains no PEM certificate", cfg.CAPath)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}, nil
}
