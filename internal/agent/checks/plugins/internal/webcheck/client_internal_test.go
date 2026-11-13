package webcheck

import (
	"net/http"
	"testing"
)

// The transport must not consult the environment's proxy variables: status
// pages are local and the basic-auth credentials must never reach a proxy.
func TestClientHasNoProxyAndDoesNotFollowRedirects(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	c := newClient(Options{VerifyTLS: true})
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", c.Transport)
	}
	if tr.Proxy != nil {
		t.Error("Transport.Proxy is set")
	}
	if !tr.DisableKeepAlives {
		t.Error("keep-alives are enabled")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion < 0x0303 {
		t.Errorf("TLS minimum is below 1.2: %+v", tr.TLSClientConfig)
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("verification is off although VerifyTLS is true")
	}
	if c.CheckRedirect == nil {
		t.Error("redirects are followed")
	}
	if newClient(Options{}) == c {
		t.Error("clients are shared")
	}
	if !newClient(Options{VerifyTLS: false}).Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Error("VerifyTLS false does not skip verification")
	}
}
