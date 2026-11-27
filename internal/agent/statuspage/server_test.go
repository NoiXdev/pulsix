package statuspage

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestController(t *testing.T, logs *bytes.Buffer) (*Controller, string) {
	t.Helper()
	dir := t.TempDir()
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	c := NewController(&fakeSource{snap: fullSnapshot()}, Options{
		DataDir: dir, Log: slog.New(slog.NewTextHandler(logs, nil)),
		Hostname: func() (string, error) { return "agent-host.test", nil },
	})
	t.Cleanup(c.Close)
	return c, dir
}

func get(t *testing.T, client *http.Client, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func noRedirect() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestControllerServesLoopbackPlainHTTP(t *testing.T) {
	c, _ := newTestController(t, nil)
	c.Apply(Config{Listen: "127.0.0.1:0"}, "")
	if c.Mode() != ModeLoopback || c.Addr() == "" {
		t.Fatalf("mode %v addr %q", c.Mode(), c.Addr())
	}
	resp, body := get(t, noRedirect(), "http://"+c.Addr()+"/status.json", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"version": "v1.2.3"`) {
		t.Fatalf("status.json = %d %s", resp.StatusCode, body)
	}
}

func TestControllerServesNetworkOverTLSOnly(t *testing.T) {
	c, dir := newTestController(t, nil)
	// A network page on a loopback port, so the test dials nothing else.
	c.Apply(Config{Listen: "127.0.0.1:0", Token: testToken, Network: true}, "")
	if c.Mode() != ModeNetwork {
		t.Fatalf("mode = %v", c.Mode())
	}
	// Plain HTTP gets no page.
	if resp, err := noRedirect().Get("http://" + c.Addr() + "/status.json"); err == nil {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(b), "v1.2.3") {
			t.Fatalf("plain HTTP answered %d", resp.StatusCode)
		}
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, selfSignedCert))
	if err != nil {
		t.Fatalf("self-signed certificate not stored: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	if resp, _ := get(t, client, "https://"+c.Addr()+"/status.json", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d", resp.StatusCode)
	}
	resp, body := get(t, client, "https://"+c.Addr()+"/status.json", map[string]string{"Authorization": "Bearer " + testToken})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "v1.2.3") {
		t.Fatalf("with token = %d", resp.StatusCode)
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{selfSignedCert, selfSignedKey} {
			fi, err := os.Stat(filepath.Join(dir, f))
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %v (%v)", f, fi.Mode(), err)
			}
		}
	}
}

func TestControllerUsesGivenCertificate(t *testing.T) {
	c, dir := newTestController(t, nil)
	// Make a certificate with the self-signed helper in another dir and
	// hand it over as tls_cert/tls_key.
	other := t.TempDir()
	if _, err := selfSigned(other, "127.0.0.1", "", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c.Apply(Config{Listen: "127.0.0.1:0", TLSCert: filepath.Join(other, selfSignedCert), TLSKey: filepath.Join(other, selfSignedKey)}, "")
	if c.Mode() != ModeLoopback {
		t.Fatalf("mode = %v", c.Mode())
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // the test's own certificate
	if resp, _ := get(t, client, "https://"+c.Addr()+"/status.json", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS = %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, selfSignedCert)); err == nil {
		t.Fatal("a self-signed certificate was made although one was given")
	}
	// A given certificate that does not load fails the page.
	var logs bytes.Buffer
	c2, _ := newTestController(t, &logs)
	c2.Apply(Config{Listen: "127.0.0.1:0", TLSCert: filepath.Join(other, "missing.crt"), TLSKey: filepath.Join(other, "missing.key")}, "")
	if c2.Mode() != ModeFailed || c2.Addr() != "" || !strings.Contains(logs.String(), "could not start") {
		t.Fatalf("mode %v addr %q logs %s", c2.Mode(), c2.Addr(), logs.String())
	}
}

func TestControllerFollowsConfigChange(t *testing.T) {
	c, _ := newTestController(t, nil)
	if c.Mode() != ModeOff {
		t.Fatalf("a new controller is %v", c.Mode())
	}
	c.Apply(Config{Listen: "127.0.0.1:0"}, "")
	first := c.Addr()
	// The same settings leave the listener alone.
	c.Apply(Config{Listen: "127.0.0.1:0"}, "")
	if c.Addr() != first {
		t.Fatalf("an unchanged Apply moved the page: %s -> %s", first, c.Addr())
	}
	// A changed one moves it, and the old address stops answering.
	c.Apply(Config{Listen: "127.0.0.1:0", Token: testToken}, "")
	if c.Addr() == "" || c.Addr() == first {
		t.Fatalf("changed settings did not move the page: %q", c.Addr())
	}
	if conn, err := net.DialTimeout("tcp", first, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("the old address still listens")
	}
	// A refused section turns it off as failed.
	c.Apply(Config{}, "line 3, column 5: whatever")
	if c.Mode() != ModeFailed || c.Addr() != "" {
		t.Fatalf("refused: mode %v addr %q", c.Mode(), c.Addr())
	}
	// No listen: off.
	c.Apply(Config{Listen: "127.0.0.1:0"}, "")
	c.Apply(Config{}, "")
	if c.Mode() != ModeOff || c.Addr() != "" {
		t.Fatalf("off: mode %v addr %q", c.Mode(), c.Addr())
	}
	// Closed: Apply does nothing.
	c.Close()
	c.Apply(Config{Listen: "127.0.0.1:0"}, "")
	if c.Mode() != ModeOff || c.Addr() != "" {
		t.Fatal("Apply after Close started the page")
	}
}

func TestControllerBindFailureIsRetriedAndLoggedOnce(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := taken.Addr().String()
	var logs bytes.Buffer
	c, _ := newTestController(t, &logs)
	for range 3 {
		c.Apply(Config{Listen: addr}, "")
	}
	if c.Mode() != ModeFailed {
		t.Fatalf("mode = %v", c.Mode())
	}
	if n := strings.Count(logs.String(), "could not start"); n != 1 {
		t.Fatalf("logged %d times", n)
	}
	_ = taken.Close()
	c.Apply(Config{Listen: addr}, "")
	if c.Mode() != ModeLoopback || c.Addr() != addr {
		t.Fatalf("after the port freed: mode %v addr %q", c.Mode(), c.Addr())
	}
}

func TestSelfSignedIsReusedAndRenewed(t *testing.T) {
	dir := t.TempDir()
	first, err := selfSigned(dir, "192.0.2.10", "agent-host.test", t0)
	if err != nil {
		t.Fatal(err)
	}
	leaf := first.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(first.Certificate[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := leaf.VerifyHostname("192.0.2.10"); err != nil {
		t.Errorf("listen address not named: %v", err)
	}
	if err := leaf.VerifyHostname("agent-host.test"); err != nil {
		t.Errorf("host name not named: %v", err)
	}
	again, err := selfSigned(dir, "192.0.2.10", "agent-host.test", t0.Add(24*time.Hour))
	if err != nil || !bytes.Equal(again.Certificate[0], first.Certificate[0]) {
		t.Fatalf("not reused: %v", err)
	}
	// A different listen address, or nearing the end, makes a new one.
	moved, err := selfSigned(dir, "192.0.2.11", "agent-host.test", t0.Add(48*time.Hour))
	if err != nil || bytes.Equal(moved.Certificate[0], first.Certificate[0]) {
		t.Fatalf("not renewed for a new address: %v", err)
	}
	late, err := selfSigned(dir, "192.0.2.11", "agent-host.test", t0.Add(48*time.Hour+selfSignedLifetime-selfSignedRenew+time.Minute))
	if err != nil || bytes.Equal(late.Certificate[0], moved.Certificate[0]) {
		t.Fatalf("not renewed near its end: %v", err)
	}
	// A broken file is replaced.
	if err := os.WriteFile(filepath.Join(dir, selfSignedCert), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := selfSigned(dir, "192.0.2.11", "agent-host.test", t0); err != nil {
		t.Fatalf("broken file not replaced: %v", err)
	}
}
