package dbcheck

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/testpki"
)

// plantedDir is part of every path the tests use; no error may carry it.
const plantedDir = "planted-secret-dir"

func tlsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), plantedDir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func files(ca, cert, key string) TLSFiles {
	return TLSFiles{CA: ca, Cert: cert, Key: key, CAField: "tls_ca", CertField: "tls_cert", KeyField: "tls_key"}
}

func mustLoadErr(t *testing.T, f TLSFiles, want string) {
	t.Helper()
	_, err := f.Load()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if strings.Contains(err.Error(), plantedDir) {
		t.Fatalf("the path leaked into %q", err)
	}
}

func TestTLSFilesLoad(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	caPath := testpki.Write(t, dir, "ca.pem", ca.PEM, 0o644)
	certPath := testpki.Write(t, dir, "client.pem", client.CertPEM, 0o644)
	keyPath := testpki.Write(t, dir, "client.key", client.KeyPEM, 0o600)

	t.Run("nothing set", func(t *testing.T) {
		m, err := files("", "", "").Load()
		if err != nil || m.RootCAs != nil || len(m.Certificates) != 0 {
			t.Fatalf("%+v, %v", m, err)
		}
	})
	t.Run("everything", func(t *testing.T) {
		m, err := files(caPath, certPath, keyPath).Load()
		if err != nil {
			t.Fatal(err)
		}
		if m.RootCAs == nil || !m.RootCAs.Equal(ca.Pool()) {
			t.Error("the CA file did not become the root pool")
		}
		if len(m.Certificates) != 1 || len(m.Certificates[0].Certificate) != 1 {
			t.Fatalf("certificates = %d", len(m.Certificates))
		}
	})
	t.Run("CA only", func(t *testing.T) {
		m, err := files(caPath, "", "").Load()
		if err != nil || m.RootCAs == nil || len(m.Certificates) != 0 {
			t.Fatalf("%+v, %v", m, err)
		}
	})
	t.Run("a chain in the certificate file", func(t *testing.T) {
		chain := testpki.Write(t, dir, "chain.pem", append(append([]byte{}, client.CertPEM...), ca.PEM...), 0o644)
		m, err := files("", chain, keyPath).Load()
		if err != nil || len(m.Certificates[0].Certificate) != 2 {
			t.Fatalf("%+v, %v", m, err)
		}
	})
	for name, block := range map[string]func() *pem.Block{
		"PKCS#1-style EC key": func() *pem.Block {
			der, err := x509.MarshalECPrivateKey(client.Key)
			if err != nil {
				t.Fatal(err)
			}
			return &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
		},
		"key followed by the certificate": func() *pem.Block {
			b, _ := pem.Decode(client.KeyPEM)
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			data := pem.EncodeToMemory(block())
			if strings.HasPrefix(name, "key followed") {
				data = append(data, client.CertPEM...)
			}
			k := testpki.Write(t, dir, "alt.key", data, 0o600)
			if _, err := files("", certPath, k).Load(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTLSFilesNeedsBothCertAndKey(t *testing.T) {
	mustLoadErr(t, files("", "/"+plantedDir+"/c.pem", ""), "secret field `tls_cert` needs `tls_key`")
	mustLoadErr(t, files("", "", "/"+plantedDir+"/c.key"), "secret field `tls_key` needs `tls_cert`")
}

func TestTLSFilesCA(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	mustLoadErr(t, files(filepath.Join(dir, "missing.pem"), "", ""), "secret field `tls_ca` cannot be read")
	mustLoadErr(t, files(testpki.Write(t, dir, "junk.pem", []byte("not pem at all"), 0o644), "", ""), "secret field `tls_ca` is not a CA file")
	// A key alone holds no certificate.
	mustLoadErr(t, files(testpki.Write(t, dir, "key.pem", client.KeyPEM, 0o600), "", ""), "secret field `tls_ca` is not a CA file")
}

func TestTLSFilesNotRegularOrTooLarge(t *testing.T) {
	dir := tlsDir(t)
	mustLoadErr(t, files(dir, "", ""), "secret field `tls_ca` is not a regular file")
	big := testpki.Write(t, dir, "big.pem", make([]byte, maxTLSFileBytes+1), 0o644)
	mustLoadErr(t, files(big, "", ""), "secret field `tls_ca` is larger than 1 MiB")
}

func TestTLSFilesCertificate(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	key := testpki.Write(t, dir, "c.key", client.KeyPEM, 0o600)
	mustLoadErr(t, files("", filepath.Join(dir, "missing.pem"), key), "secret field `tls_cert` cannot be read")
	mustLoadErr(t, files("", testpki.Write(t, dir, "junk.pem", []byte("junk"), 0o644), key), "secret field `tls_cert` is not a certificate file")
	mustLoadErr(t, files("", testpki.Write(t, dir, "k.pem", client.KeyPEM, 0o600), key), "secret field `tls_cert` is not a certificate file")
	bad := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")})
	mustLoadErr(t, files("", testpki.Write(t, dir, "bad.pem", bad, 0o644), key), "secret field `tls_cert` is not a certificate file")
}

func TestTLSFilesKeyNotAKey(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	cert := testpki.Write(t, dir, "c.pem", client.CertPEM, 0o644)
	mustLoadErr(t, files("", cert, filepath.Join(dir, "missing.key")), "secret field `tls_key` cannot be read")
	mustLoadErr(t, files("", cert, testpki.Write(t, dir, "junk.key", []byte("junk"), 0o600)), "secret field `tls_key` is not a private key file")
	// The certificate first: only EC PARAMETERS may come before the key.
	both := append(append([]byte{}, client.CertPEM...), client.KeyPEM...)
	mustLoadErr(t, files("", cert, testpki.Write(t, dir, "both.key", both, 0o600)), notFirst)
	bad := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	mustLoadErr(t, files("", cert, testpki.Write(t, dir, "bad.key", bad, 0o600)), "secret field `tls_key` is not a private key file")
}

// notFirst is the reason for a key file whose key comes after other blocks.
const notFirst = "secret field `tls_key` does not start with the private key; remove the PEM blocks before it (such as EC PARAMETERS)"

// ecparamKey is what `openssl ecparam -name prime256v1 -genkey` writes: an
// EC PARAMETERS block (the curve's OID) followed by the SEC 1 key.
func ecparamKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	oid, err := asn1.Marshal(asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7})
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "EC PARAMETERS", Bytes: oid}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})...)
}

func TestTLSFilesKeyWithECParameters(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	cert := testpki.Write(t, dir, "c.pem", client.CertPEM, 0o644)
	key := testpki.Write(t, dir, "ecparam.key", ecparamKey(t, client.Key), 0o600)
	m, err := files("", cert, key).Load()
	if err != nil || len(m.Certificates) != 1 {
		t.Fatalf("%+v, %v", m, err)
	}
	// Where only the first block is read (pgx), the parameters must go.
	f := files("", cert, key)
	f.FirstBlockOnly = true
	mustLoadErr(t, f, notFirst)
	// Parameters alone are no key.
	params, _ := pem.Decode(ecparamKey(t, client.Key))
	mustLoadErr(t, files("", cert, testpki.Write(t, dir, "params.key", pem.EncodeToMemory(params), 0o600)), "secret field `tls_key` is not a private key file")
}

func TestTLSFilesKeyEncrypted(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	cert := testpki.Write(t, dir, "c.pem", ca.Client(t, "mon").CertPEM, 0o644)
	want := "secret field `tls_key` is an encrypted key, which is not supported; store it unencrypted with owner-only permissions"
	p8 := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0x30, 0x00}})
	mustLoadErr(t, files("", cert, testpki.Write(t, dir, "p8.key", p8, 0o600)), want)
	legacy := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00112233445566778899AABBCCDDEEFF"},
		Bytes:   []byte("ciphertext"),
	})
	mustLoadErr(t, files("", cert, testpki.Write(t, dir, "legacy.key", legacy, 0o600)), want)
}

func TestTLSFilesKeyMismatch(t *testing.T) {
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	cert := testpki.Write(t, dir, "c.pem", ca.Client(t, "mon").CertPEM, 0o644)
	other := testpki.Write(t, dir, "other.key", ca.Client(t, "other").KeyPEM, 0o600)
	mustLoadErr(t, files("", cert, other), "secret field `tls_key` does not match `tls_cert`")
}

func TestTLSFilesKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keys are guarded by ACLs, not mode bits")
	}
	dir := tlsDir(t)
	ca := testpki.NewCA(t)
	client := ca.Client(t, "mon")
	cert := testpki.Write(t, dir, "c.pem", client.CertPEM, 0o644)
	want := "secret field `tls_key` is accessible to other users; remove their permissions, for example chmod 600"
	for _, mode := range []os.FileMode{0o644, 0o604, 0o602, 0o601} {
		k := testpki.Write(t, dir, fmt.Sprintf("open-%o.key", mode), client.KeyPEM, mode)
		mustLoadErr(t, files("", cert, k), want)
	}
	for _, mode := range []os.FileMode{0o600, 0o640, 0o400, 0o440} {
		k := testpki.Write(t, dir, fmt.Sprintf("ok-%o.key", mode), client.KeyPEM, mode)
		if _, err := files("", cert, k).Load(); err != nil {
			t.Errorf("mode %o: %v", mode, err)
		}
	}
}

func TestAbsPath(t *testing.T) {
	abs := "/etc/ssl/ca.pem"
	if runtime.GOOS == "windows" {
		abs = `C:\certs\ca.pem`
	}
	for s, want := range map[string]bool{abs: true, "ca.pem": false, "./ca.pem": false, "": false, abs + "\x00": false} {
		if got := AbsPath(s); got != want {
			t.Errorf("AbsPath(%q) = %v, want %v", s, got, want)
		}
	}
}
