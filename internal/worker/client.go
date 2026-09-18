// The worker's mTLS client: one TLS 1.3 connection to the manager's gRPC
// endpoint, authenticated on both ends by certificates the instance CA
// issued (spec §14). The client pins the server by the CA pool it was
// enrolled with and presents its current identity on every handshake
// (tls.Config.GetClientCertificate), so a renewal takes effect on the next
// connection without restarting the client.
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

// Dial loads the worker's identity (CertPath/KeyPath) and opens the
// manager's gRPC endpoint with it. The connection is lazy — gRPC
// establishes it at the first RPC and keeps re-establishing it, so a
// manager restart heals without the loop's help; bad TLS material on the
// other hand fails right here.
func Dial(ctx context.Context, cfg Config) (*grpc.ClientConn, error) {
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	return dialIdentity(ctx, cfg, id, nil)
}

// DialIdentity is Dial with an Identity the caller keeps (cmd/worker hands
// the same one to the Renewer, whose Replace the next handshake picks up).
func DialIdentity(ctx context.Context, cfg Config, id *Identity) (*grpc.ClientConn, error) {
	return dialIdentity(ctx, cfg, id, nil)
}

// dial keeps the test harness's entry point: it loads the identity itself.
func dial(ctx context.Context, cfg Config, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	id, err := LoadIdentity(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	return dialIdentity(ctx, cfg, id, extra)
}

// serverNameOf is the TLS ServerName: cfg.ServerName when set, otherwise
// the host part of ManagerAddr (see Config.ServerName's own doc comment).
func serverNameOf(cfg Config) (string, error) {
	if cfg.ServerName != "" {
		return cfg.ServerName, nil
	}
	host, _, err := net.SplitHostPort(cfg.ManagerAddr)
	if err != nil {
		return "", fmt.Errorf("resolve server name from %q: %w", cfg.ManagerAddr, err)
	}
	return host, nil
}

// dialIdentity builds the connection; extra dial options exist for the
// bufconn test harness.
func dialIdentity(_ context.Context, cfg Config, id *Identity, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	caPEM, err := os.ReadFile(cfg.CAPath)
	if err != nil {
		return nil, fmt.Errorf("read CA pool file %s: %w", cfg.CAPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA pool file %s contains no PEM certificate", cfg.CAPath)
	}
	serverName, err := serverNameOf(cfg)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
		// Every handshake presents the identity's current certificate, so a
		// renewal takes effect on the next connection.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return id.Certificate(), nil
		},
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
