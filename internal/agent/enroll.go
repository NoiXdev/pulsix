// Enrollment and renewal: the two ways the loop's identity changes.
// Enroll spends an enrollment token over first-connect TLS pinned to the
// token's CA fingerprint (the worker's enrollment flow, mirrored); the
// key is generated ONCE and reused for every renewal. Renewal is
// certificate-authenticated and swaps the same key's certificate at
// two-thirds of its lifetime.
package agent

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/noixdev/pulsix/internal/enrolltoken"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// ErrSetupFailed marks a LOCAL setup failure the enrollment path hit
// before ever reaching the manager — an unwritable data dir, an
// unparseable persisted key — so the loop can report it as an operator
// fix instead of misclassifying it as a manager refusal or retrying it
// forever.
var ErrSetupFailed = errors.New("agent setup failed")

// ErrBadEnrollAnswer marks a manager ANSWER the loop refused: the
// enrollment RPC completed, but the returned material (CA chain,
// certificate) failed verification. No retry fixes a broken answer, so
// the loop reports it like any other refusal instead of retrying.
var ErrBadEnrollAnswer = errors.New("the manager returned unusable enrollment material")

// enroll exchanges the token for the agent's first client certificate:
// the key is generated ONCE (a surviving key file is reused), the CSR
// rides a TLS connection pinned to the token's CA fingerprint, and the
// returned material is verified before a byte is persisted — a broken
// response must not strand an already-spent token use. Local setup
// failures (data dir, key) come back wrapped in ErrSetupFailed; the
// manager's own answers are passed through for the caller to classify.
func (l *Loop) enroll(ctx context.Context, existing *Identity) (*Identity, error) {
	tok, err := enrolltoken.ParseAgent(l.cfg.EnrollToken)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", envEnrollToken, err)
	}
	// The token is at most a handful of uses: check the data dir can
	// actually be written BEFORE spending an attempt on it.
	if err := os.MkdirAll(l.cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create data dir %s: %v", ErrSetupFailed, l.cfg.DataDir, err)
	}
	if err := probeWritable(l.cfg.DataDir); err != nil {
		return nil, fmt.Errorf("%w: data dir %s is read-only (mount it writable): %v", ErrSetupFailed, l.cfg.DataDir, err)
	}
	key, err := LoadOrGenerateKey(l.cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSetupFailed, err)
	}
	csrPEM, err := MakeCSR(key, l.cfg.AgentName)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSetupFailed, err)
	}
	conn, err := l.dial.DialEnroll(ctx, tok)
	if err != nil {
		return nil, fmt.Errorf("dial manager to enroll: %w", err)
	}
	defer func() { _ = conn.Close() }()
	resp, err := conn.Enroll(ctx, &agentpb.EnrollRequest{
		Token: l.cfg.EnrollToken, CsrPem: string(csrPEM), Name: l.cfg.AgentName, Version: l.cfg.Version,
	})
	if err != nil {
		return nil, err
	}
	// Only the pin-verified CA block is trusted and written — the rest of
	// whatever the manager sent as "ca_pem" is not, because the mTLS dial
	// later loads this same file wholesale as a root pool.
	caBlock, _ := pem.Decode([]byte(resp.GetCaPem()))
	if caBlock == nil || enrolltoken.Pin(caBlock.Bytes) != tok.CAPin {
		return nil, fmt.Errorf("%w: the manager returned a CA that does not match the enrollment token", ErrBadEnrollAnswer)
	}
	leafBlock, _ := pem.Decode([]byte(resp.GetCertPem()))
	if leafBlock == nil || leafBlock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%w: the manager returned no usable certificate", ErrBadEnrollAnswer)
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse the manager's certificate: %v", ErrBadEnrollAnswer, err)
	}
	pub, eq := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !eq || !pub.Equal(key.Public()) {
		return nil, fmt.Errorf("%w: the manager's certificate does not match the key just generated", ErrBadEnrollAnswer)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBlock.Bytes})
	if err := SaveEnrollment(l.cfg.DataDir, key, []byte(resp.GetCertPem()), caPEM, resp.GetHostId()); err != nil {
		return nil, fmt.Errorf("%w: persist agent identity: %v", ErrSetupFailed, err)
	}
	fresh, err := LoadIdentity(l.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if fresh == nil {
		return nil, fmt.Errorf("%w: the freshly enrolled identity did not load", ErrBadEnrollAnswer)
	}
	l.log.InfoContext(ctx, "agent enrolled", "host_id", resp.GetHostId(), "data_dir", l.cfg.DataDir)
	return fresh, nil
}

// renew swaps the certificate at two-thirds of its lifetime: the CSR is
// signed with the SAME persisted key (the key is generated once), the
// call authenticates with the current certificate, and the fresh one
// takes effect on the next connection.
func (l *Loop) renew(ctx context.Context, id *Identity) (*Identity, error) {
	signer, ok := id.Signer().(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("agent key is %T, want *ecdsa.PrivateKey", id.Signer())
	}
	csrPEM, err := MakeCSR(signer, l.cfg.AgentName)
	if err != nil {
		return nil, err
	}
	conn, err := l.dial.DialAgent(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("dial manager to renew: %w", err)
	}
	defer func() { _ = conn.Close() }()
	resp, err := conn.Renew(ctx, &agentpb.RenewRequest{CsrPem: string(csrPEM)})
	if err != nil {
		return nil, err
	}
	if err := id.Replace([]byte(resp.GetCertPem())); err != nil {
		return nil, fmt.Errorf("apply renewed certificate: %w", err)
	}
	l.log.InfoContext(ctx, "agent certificate renewed", "host_id", id.HostID())
	return id, nil
}

// isEnrollUnreachable classifies an Enroll failure: true only for a
// genuine transport-level problem (the manager unreachable, a timeout —
// the loop waits it out) as opposed to the manager having actively
// answered with a refusal (an expired, used or unknown token; a taken
// name; a rate limit) or the client-side TLS handshake itself failing
// because the presented CA does not match the token's pinned fingerprint
// — both configuration problems no retry fixes.
//
// A pin failure never reaches an RPC status: the pinned verifier fails
// the handshake itself, and grpc-go reports that as a transport error,
// usually coded Unavailable — indistinguishable BY CODE from a real
// "manager unreachable". It is still distinguishable by message: the
// pinned verifier's own error strings ("… to pin against", "… the
// fingerprint …", "… the pinned CA …") are matched exactly, and ONLY
// those — grpc-go's own "authentication handshake failed" prefix is
// generic to every TLS handshake failure, including a transient one (an
// EOF or a connection reset while the manager is still starting up), so
// matching that prefix alone would misclassify those as a refusal too.
func isEnrollUnreachable(err error) bool {
	msg := err.Error()
	for _, s := range []string{"pinned CA", "fingerprint", "to pin against"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	st, ok := status.FromError(err)
	if !ok {
		return true // no gRPC status at all: a raw dial/network error
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unknown:
		return true
	default:
		// Unauthenticated (enrollment refused), AlreadyExists (name
		// taken), InvalidArgument (bad CSR), ResourceExhausted (rate
		// limited), … — the manager answered with a definite refusal.
		return false
	}
}
