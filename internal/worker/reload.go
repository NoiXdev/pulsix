// A bundle rewritten under a running worker (final review I2b). The
// compose stack's `worker add --replace` rewrites the certificate, key
// and CA when the database does not know the worker's certificate any
// more — a restore older than the worker's last renewal, or a new server
// set up before the restore. The worker re-reads the three files before
// every handshake, so the next connection uses the new bundle without
// recreating the container.
package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Reload re-reads the certificate and key files and installs them when
// the certificate on disk is no longer the one this Identity last saw
// there. It reports whether it changed the identity. A pair that does not
// load (a bundle caught half-written) is an error and changes nothing;
// the next call tries again. A renewal still waiting to be written
// (HasPendingWrite) is newer than the file, so the file is ignored then.
func (i *Identity) Reload() (bool, error) {
	pair, err := tls.LoadX509KeyPair(i.certPath, i.keyPath)
	if err != nil {
		return false, fmt.Errorf("reload worker certificate (%s, %s): %w", i.certPath, i.keyPath, err)
	}
	if pair.Leaf == nil {
		if pair.Leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return false, fmt.Errorf("parse worker certificate %s: %w", i.certPath, err)
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.pending != nil || bytes.Equal(pair.Leaf.Raw, i.diskLeaf.Raw) {
		return false, nil
	}
	i.cert = &pair
	i.diskLeaf = pair.Leaf
	return true, nil
}

// RedialOnNewIdentity is the loop's WithOnUnauthenticated hook for
// cmd/worker: when the identity files changed on disk, it installs them
// and moves the client to a fresh connection that handshakes with them
// (an established connection keeps the certificate it handshook with).
// It reports whether it did.
func RedialOnNewIdentity(id *Identity, client *ReconnectingClient, dial func(ctx context.Context) (*grpc.ClientConn, error), log *slog.Logger) func(ctx context.Context) bool {
	return func(ctx context.Context) bool {
		changed, err := id.Reload()
		if err != nil {
			log.WarnContext(ctx, "re-read the worker certificate bundle", "err", err)
			return false
		}
		if !changed {
			return false
		}
		log.InfoContext(ctx, "the worker certificate bundle changed on disk; reconnecting with it", "serial", id.Leaf().SerialNumber)
		client.Redial(ctx, dial)
		return true
	}
}

// reloadingTLS is the dial's transport credentials: before every
// handshake (the first one and each reconnect gRPC makes on its own) it
// re-reads the identity files and the CA file, then hands the handshake
// to a standard TLS configuration built from them. A CA file that does
// not read keeps the last one that did; an identity that does not load
// keeps the current one (Reload).
type reloadingTLS struct {
	cfg        Config
	id         *Identity
	serverName string

	mu   sync.Mutex
	pool *x509.CertPool
}

func newReloadingTLS(cfg Config, id *Identity, serverName string, pool *x509.CertPool) *reloadingTLS {
	return &reloadingTLS{cfg: cfg, id: id, serverName: serverName, pool: pool}
}

// tlsConfig is the configuration of one handshake: TLS 1.3, the current
// CA pool, and the identity's current certificate. GetClientCertificate
// reads the identity at handshake time, so a renewal swapped in by the
// Renewer is presented by the next connection as before.
func (c *reloadingTLS) tlsConfig() *tls.Config {
	c.mu.Lock()
	pool, serverName := c.pool, c.serverName
	c.mu.Unlock()
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: serverName,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return c.id.Certificate(), nil
		},
	}
}

// refresh re-reads the bundle. Failures are not fatal here: the
// handshake goes ahead with what the worker has, and the next one reads
// the files again.
func (c *reloadingTLS) refresh() {
	_, _ = c.id.Reload()
	if pool, err := CAPool(c.cfg); err == nil {
		c.mu.Lock()
		c.pool = pool
		c.mu.Unlock()
	}
}

func (c *reloadingTLS) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	c.refresh()
	return credentials.NewTLS(c.tlsConfig()).ClientHandshake(ctx, authority, raw)
}

func (c *reloadingTLS) ServerHandshake(net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("the worker's transport credentials are client-only")
}

func (c *reloadingTLS) Info() credentials.ProtocolInfo {
	return credentials.NewTLS(c.tlsConfig()).Info()
}

func (c *reloadingTLS) Clone() credentials.TransportCredentials {
	c.mu.Lock()
	defer c.mu.Unlock()
	return newReloadingTLS(c.cfg, c.id, c.serverName, c.pool)
}

// OverrideServerName is deprecated in gRPC and unused here; it sets the
// name the next handshake verifies, like the standard TLS credentials.
func (c *reloadingTLS) OverrideServerName(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serverName = name
	return nil
}
