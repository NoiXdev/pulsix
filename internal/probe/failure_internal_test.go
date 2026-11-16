package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"testing"
)

// timeoutError is a net.Error that timed out, like a read past its
// deadline.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func dialErr(err error) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", err)}
}

func readErr(err error) error {
	return &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", err)}
}

// urlErr wraps err the way the http client does, its URL quoted.
func urlErr(err error) error {
	return &url.Error{Op: "Get", URL: "https://example.test/?token=s3cret", Err: err}
}

func TestClassify(t *testing.T) {
	type tc struct {
		name string
		err  error
		want string
	}
	cases := []tc{
		{"nil is an error without a cause", nil, FailureError},
		{"unknown", errors.New("boom"), FailureError},
		{"closed connection", net.ErrClosed, FailureError},
		{"context deadline", context.DeadlineExceeded, FailureTimeout},
		{"wrapped context deadline", urlErr(fmt.Errorf("x: %w", context.DeadlineExceeded)), FailureTimeout},
		{"os deadline", readErr(os.ErrDeadlineExceeded), FailureTimeout},
		{"net timeout", &net.OpError{Op: "dial", Err: timeoutError{}}, FailureTimeout},
		{"name not found", dialErr(&net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}), FailureDNS},
		{"resolver timeout", &net.DNSError{Err: "i/o timeout", Name: "x.test", IsTimeout: true}, FailureDNS},
		{"resolver gave up at the deadline", &net.DNSError{Err: "x", Name: "x.test", UnwrapErr: context.DeadlineExceeded}, FailureDNS},
		{"eof", io.EOF, FailureReset},
		{"unexpected eof", urlErr(io.ErrUnexpectedEOF), FailureReset},
		{"unknown authority", x509.UnknownAuthorityError{}, FailureCertificate},
		{"hostname", x509.HostnameError{Host: "x.test"}, FailureCertificate},
		{"expired", x509.CertificateInvalidError{Reason: x509.Expired}, FailureCertificate},
		{"verification error", urlErr(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), FailureCertificate},
		{"record header", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, FailureTLS},
		{"remote alert", &net.OpError{Op: "remote error", Err: errors.New("tls: handshake failure")}, FailureTLS},
		{"quic alert", fmt.Errorf("x%.0w", tls.AlertError(40)), FailureTLS},
	}
	for _, e := range refusedErrnos {
		cases = append(cases, tc{"refused " + e.Error(), dialErr(e), FailureRefused})
	}
	for _, e := range resetErrnos {
		cases = append(cases, tc{"reset " + e.Error(), urlErr(readErr(e)), FailureReset})
	}
	for _, e := range unreachableErrnos {
		cases = append(cases, tc{"unreachable " + e.Error(), dialErr(e), FailureUnreachable})
	}
	for _, e := range timeoutErrnos {
		cases = append(cases, tc{"timed out " + e.Error(), dialErr(e), FailureTimeout})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.err); got != c.want {
				t.Fatalf("Classify(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

// TestClassifyHandshake: a handshake that failed for no network reason
// failed as TLS; the network reasons and a certificate keep their labels.
func TestClassifyHandshake(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"unsupported version", errors.New("tls: server selected unsupported protocol version 300"), FailureTLS},
		{"nil", nil, FailureTLS},
		{"eof", io.EOF, FailureReset},
		{"deadline", context.DeadlineExceeded, FailureTimeout},
		{"certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, FailureCertificate},
		{"refused", dialErr(refusedErrnos[0]), FailureRefused},
	} {
		if got := ClassifyHandshake(c.err); got != c.want {
			t.Errorf("%s: ClassifyHandshake = %q, want %q", c.name, got, c.want)
		}
	}
}
