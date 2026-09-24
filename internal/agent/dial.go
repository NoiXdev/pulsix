// The agent's production dialer: gRPC over TLS to the manager's shared
// gRPC endpoint. Two connection kinds — the enrollment call rides
// first-connect TLS pinned to the token's CA fingerprint (no system
// roots, no trust on first use — the worker's enrollment flow mirrored),
// every other call rides mTLS with the enrolled identity presented on
// each handshake (tls.Config.GetClientCertificate), so a renewal takes
// effect on the next connection.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// grpcDialer is the production Dialer. extra carries test dial options
// (the bufconn harness routes the transport through memory).
type grpcDialer struct {
	cfg   Config
	extra []grpc.DialOption
}

// NewDialer builds the production dialer for cfg.
func NewDialer(cfg Config) Dialer { return &grpcDialer{cfg: cfg} }

// DialEnroll dials pinned to the token's CA fingerprint.
func (d *grpcDialer) DialEnroll(_ context.Context, tok enrolltoken.Token) (EnrollClient, error) {
	serverName, err := serverNameOf(d.cfg)
	if err != nil {
		return nil, err
	}
	tlsCfg := pinnedTLS(tok.CAPin, serverName)
	return d.dial(tlsCfg)
}

// DialAgent dials mTLS with the enrolled identity and the persisted CA
// chain as the trust root.
func (d *grpcDialer) DialAgent(_ context.Context, id *Identity) (AgentClient, error) {
	caPEM, err := os.ReadFile(filepath.Join(d.cfg.DataDir, caFileName))
	if err != nil {
		return nil, fmt.Errorf("read CA chain file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("CA chain file %s contains no PEM certificate", caFileName)
	}
	serverName, err := serverNameOf(d.cfg)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
		// Every handshake presents the identity's current certificate, so
		// a renewal takes effect on the next connection.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return id.Certificate(), nil
		},
	}
	return d.dial(tlsCfg)
}

// dial builds one lazy gRPC connection (the handshake happens at the
// first RPC — bad TLS material fails the first call, not the dial).
func (d *grpcDialer) dial(tlsCfg *tls.Config) (*grpcAgentClient, error) {
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	}, d.extra...)
	conn, err := grpc.NewClient("passthrough:///"+d.cfg.ManagerAddr, opts...)
	if err != nil {
		return nil, fmt.Errorf("new gRPC client for %s: %w", d.cfg.ManagerAddr, err)
	}
	return &grpcAgentClient{client: agentpb.NewAgentServiceClient(conn), conn: conn}, nil
}

// grpcAgentClient is one live connection: the narrow slice of
// agentpb.AgentServiceClient the loop needs, plus Close. Both
// EnrollClient and AgentClient narrow it (Enroll rides the pinned
// connection, Renew/AgentStream the mTLS one).
type grpcAgentClient struct {
	client agentpb.AgentServiceClient
	conn   *grpc.ClientConn
}

func (g *grpcAgentClient) Enroll(ctx context.Context, req *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
	return g.client.Enroll(ctx, req)
}

func (g *grpcAgentClient) Renew(ctx context.Context, req *agentpb.RenewRequest) (*agentpb.EnrollResponse, error) {
	return g.client.Renew(ctx, req)
}

func (g *grpcAgentClient) AgentStream(ctx context.Context) (Stream, error) {
	return g.client.AgentStream(ctx)
}

func (g *grpcAgentClient) Close() error { return g.conn.Close() }

// serverNameOf is the TLS ServerName: cfg.ServerName when set, otherwise
// the host part of ManagerAddr.
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

// pinnedTLS is the TLS configuration of the enrollment call: no system
// roots and no trust on first use — the manager's chain must lead to the
// CA whose SHA-256 the enrollment token carries.
func pinnedTLS(pin [32]byte, serverName string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: serverName,
		// Verification is verifyPinned below; the standard verifier would
		// consult roots the agent must not trust here.
		InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyPeerCertificate against the pinned CA
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			return verifyPinned(raw, pin, serverName)
		},
	}
}

// verifyPinned requires the presented chain to contain the pinned CA and
// the leaf to verify against it for serverName.
func verifyPinned(raw [][]byte, pin [32]byte, serverName string) error {
	if len(raw) < 2 {
		return errors.New("the manager presented no CA certificate to pin against")
	}
	var root *x509.Certificate
	for _, der := range raw[1:] {
		if enrolltoken.Pin(der) == pin {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				return fmt.Errorf("parse pinned CA: %w", err)
			}
			root = c
		}
	}
	if root == nil {
		return errors.New("the manager's CA does not match the fingerprint in the enrollment token")
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return fmt.Errorf("parse server certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: serverName, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("verify server certificate against the pinned CA: %w", err)
	}
	return nil
}
