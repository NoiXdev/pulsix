package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
)

// The failure vocabulary: why an attempt failed, reported on
// <plugin>.failure next to <plugin>.up = 0 (spec 2026-11-16, "Failure
// reason on every synthetic plugin"). A label never carries error text: a
// library's error can quote a URL with a token, a packet or an internal
// address.
const (
	// FailureNone: the attempt succeeded.
	FailureNone = "none"
	// FailureDNS: the target's name did not resolve.
	FailureDNS = "dns"
	// FailureRefused: the connection was refused (a TCP reset to the
	// connect, an ICMP port unreachable for UDP).
	FailureRefused = "refused"
	// FailureTimeout: no answer within the timeout.
	FailureTimeout = "timeout"
	// FailureUnreachable: the network or host is unreachable, no route.
	FailureUnreachable = "unreachable"
	// FailureReset: the connection was reset or closed early.
	FailureReset = "reset"
	// FailureTLS: the TLS handshake failed.
	FailureTLS = "tls"
	// FailureCertificate: the certificate did not verify.
	FailureCertificate = "certificate"
	// FailureProtocol: the response was not valid HTTP.
	FailureProtocol = "protocol"
	// FailureRedirects: more redirects than the probe follows.
	FailureRedirects = "redirects"
	// FailureNXDomain: the queried name does not exist.
	FailureNXDomain = "nxdomain"
	// FailureServFail: the DNS server answered with an error code.
	FailureServFail = "servfail"
	// FailureAuth: the SNMP agent refused the credentials.
	FailureAuth = "auth"
	// FailureAuthOrLost: a SNMPv3 device answered the engine discovery,
	// but neither the first request nor its second attempt got an answer
	// the poll could use: the device did not accept the credentials (an
	// unknown user, a wrong authentication or privacy password — none of
	// which gosnmp names once it knows the engine), or the requests were
	// lost.
	FailureAuthOrLost = "auth_or_lost"
	// FailureError: any other failure.
	FailureError = "error"
)

// FailureLabels is the whole vocabulary, in the spec's order.
func FailureLabels() []string {
	return []string{
		FailureNone, FailureDNS, FailureRefused, FailureTimeout, FailureUnreachable, FailureReset,
		FailureTLS, FailureCertificate, FailureProtocol, FailureRedirects, FailureNXDomain,
		FailureServFail, FailureAuth, FailureAuthOrLost, FailureError,
	}
}

// Classify is the failure label of err, the error a failed attempt broke
// off with: a dial, a read, a TLS handshake or a request. Only the label
// leaves the probe, never err's text. nil, and an error it does not know,
// is FailureError.
//
// A *net.DNSError is the target's name not resolving (FailureDNS); the
// dns probe, whose lookup is the point, sorts its resolver's errors
// itself.
func Classify(err error) string {
	if err == nil {
		return FailureError
	}
	if isCertificateError(err) {
		return FailureCertificate
	}
	if _, ok := errors.AsType[*net.DNSError](err); ok {
		return FailureDNS
	}
	if errnoIn(err, refusedErrnos) {
		return FailureRefused
	}
	if errnoIn(err, unreachableErrnos) {
		return FailureUnreachable
	}
	if errnoIn(err, resetErrnos) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return FailureReset
	}
	if isTimeout(err) {
		return FailureTimeout
	}
	if isTLSError(err) {
		return FailureTLS
	}
	return FailureError
}

// ClassifyHandshake is the failure label of a TLS handshake's err: as
// Classify, except that a handshake failing for no reason Classify knows
// failed as TLS (Go's client reports most of its refusals as plain text,
// "tls: ...").
func ClassifyHandshake(err error) string {
	if l := Classify(err); l != FailureError {
		return l
	}
	return FailureTLS
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) || errnoIn(err, timeoutErrnos) {
		return true
	}
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

// isCertificateError reports a certificate that did not verify: Go's TLS
// client wraps the verifier's error in a CertificateVerificationError, a
// probe verifying a chain itself gets the x509 errors directly.
func isCertificateError(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.ConstraintViolationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnhandledCriticalExtension](err); ok {
		return true
	}
	_, ok := errors.AsType[x509.SystemRootsError](err)
	return ok
}

// isTLSError reports an error only TLS raises: a record that is not TLS,
// an alert the peer sent (Go reports it as a "remote error"), or an alert
// wrapped as AlertError.
func isTLSError(err error) bool {
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.AlertError](err); ok {
		return true
	}
	if oe, ok := errors.AsType[*net.OpError](err); ok && oe.Op == "remote error" {
		return true
	}
	return false
}

func errnoIn(err error, errnos []error) bool {
	for _, e := range errnos {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
