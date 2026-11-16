package http

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http/httptrace"
	"sync"

	"github.com/noixdev/pulsix/internal/probe"
)

// hopTrace follows the last hop of a request (a redirect starts a new
// one) far enough to tell why it failed: whether its connection was made
// and whether its TLS handshake failed. Its hooks may run on the
// transport's goroutines.
type hopTrace struct {
	mu        sync.Mutex
	connected bool
	tlsErr    error
}

func (h *hopTrace) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.connected, h.tlsErr = false, nil
		},
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				return
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			h.connected = true
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				return
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			h.tlsErr = err
		},
	}
}

// reason is the failure label of a request that failed with err. Too
// many redirects is FailureRedirects; a failed handshake is classified as
// one (a certificate that did not verify is FailureCertificate); an error
// before the connection was made is classified as a dial's; once
// connected, an error that is neither the network's nor the deadline's is
// a response that was not HTTP (net/http reports those as text only).
func (h *hopTrace) reason(err error) string {
	if errors.Is(err, errTooManyRedirects) {
		return probe.FailureRedirects
	}
	h.mu.Lock()
	connected, tlsErr := h.connected, h.tlsErr
	h.mu.Unlock()
	if tlsErr != nil {
		return probe.ClassifyHandshake(tlsErr)
	}
	l := probe.Classify(err)
	if !connected || l != probe.FailureError || errors.Is(err, context.Canceled) {
		return l
	}
	return probe.FailureProtocol
}
