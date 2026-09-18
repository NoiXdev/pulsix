package worker

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/enrolltoken"
)

func TestVerifyPinned(t *testing.T) {
	h := newHarness(t)
	_, root := h.ca.Pool()
	certPEM, _, _, err := h.ca.Issue(context.Background(), nil, uuid.Nil, "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := parseCertPEM(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	pin := enrolltoken.Pin(root.Raw)
	if err := verifyPinned([][]byte{leaf.Raw, root.Raw}, pin, "manager"); err != nil {
		t.Fatalf("the pinned chain must verify: %v", err)
	}
	for name, c := range map[string]struct {
		raw        [][]byte
		pin        [32]byte
		serverName string
	}{
		"no CA sent":        {[][]byte{leaf.Raw}, pin, "manager"},
		"other fingerprint": {[][]byte{leaf.Raw, root.Raw}, enrolltoken.Pin([]byte("x")), "manager"},
		"wrong host":        {[][]byte{leaf.Raw, root.Raw}, pin, "evil.example"},
		"garbage leaf":      {[][]byte{[]byte("x"), root.Raw}, pin, "manager"},
	} {
		if err := verifyPinned(c.raw, c.pin, c.serverName); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func parseCertPEM(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	return x509.ParseCertificate(block.Bytes)
}
