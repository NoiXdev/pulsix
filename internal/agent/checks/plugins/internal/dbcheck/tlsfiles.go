package dbcheck

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxTLSFileBytes bounds one CA, certificate or key file; a system CA
// bundle is about 200 KiB.
const maxTLSFileBytes = 1 << 20

// AbsPath reports whether s is an absolute path on the agent's system
// without NUL bytes. A relative path would resolve against the service's
// working directory, which differs between service managers.
func AbsPath(s string) bool { return noNUL(s) && filepath.IsAbs(s) }

func noNUL(s string) bool { return !strings.ContainsRune(s, 0) }

// TLSFiles names the file fields of a secret's TLS settings: the paths
// (empty when unset) and the field names errors refer to.
type TLSFiles struct {
	CA, Cert, Key                string
	CAField, CertField, KeyField string
}

// TLSMaterial is what TLSFiles.Load read: the CA file as a pool (nil
// without one, meaning the system roots) and the client certificate (none
// without one).
type TLSMaterial struct {
	RootCAs      *x509.CertPool
	Certificates []tls.Certificate
}

func fieldErr(field, reason string) error {
	return errors.New("secret field `" + field + "` " + reason)
}

// Load reads and checks the files. Every error names a field and a fixed
// reason, never the path or anything read from the file. The client
// certificate and key come as a pair; the key is refused when other users
// may access it (Unix), when it is encrypted and when it does not belong
// to the certificate.
func (f TLSFiles) Load() (TLSMaterial, error) {
	var m TLSMaterial
	switch {
	case f.Cert != "" && f.Key == "":
		return m, fieldErr(f.CertField, "needs `"+f.KeyField+"`")
	case f.Key != "" && f.Cert == "":
		return m, fieldErr(f.KeyField, "needs `"+f.CertField+"`")
	}
	if f.CA != "" {
		data, err := readTLSFile(f.CA, f.CAField, false)
		if err != nil {
			return m, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return m, fieldErr(f.CAField, "is not a CA file")
		}
		m.RootCAs = pool
	}
	if f.Cert != "" {
		cert, err := f.loadPair()
		if err != nil {
			return m, err
		}
		m.Certificates = []tls.Certificate{cert}
	}
	return m, nil
}

func (f TLSFiles) loadPair() (tls.Certificate, error) {
	certPEM, err := readTLSFile(f.Cert, f.CertField, false)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !holdsCertificates(certPEM) {
		return tls.Certificate{}, fieldErr(f.CertField, "is not a certificate file")
	}
	keyData, err := readTLSFile(f.Key, f.KeyField, true)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := firstKeyBlock(keyData, f.KeyField)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fieldErr(f.KeyField, "does not match `"+f.CertField+"`")
	}
	return cert, nil
}

// readTLSFile reads a regular file of at most maxTLSFileBytes. A key file
// is refused first when other users have any permission on it.
func readTLSFile(path, field string, isKey bool) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fieldErr(field, "cannot be read")
	}
	if !fi.Mode().IsRegular() {
		return nil, fieldErr(field, "is not a regular file")
	}
	if fi.Size() > maxTLSFileBytes {
		return nil, fieldErr(field, "is larger than 1 MiB")
	}
	if isKey && !keyModeOK(fi.Mode()) {
		return nil, fieldErr(field, "is accessible to other users; remove their permissions, for example chmod 600")
	}
	fh, err := os.Open(path) // #nosec G304 -- the operator names the file in agent.yaml
	if err != nil {
		return nil, fieldErr(field, "cannot be read")
	}
	defer func() { _ = fh.Close() }()
	data, err := io.ReadAll(io.LimitReader(fh, maxTLSFileBytes+1))
	if err != nil {
		return nil, fieldErr(field, "cannot be read")
	}
	if len(data) > maxTLSFileBytes {
		return nil, fieldErr(field, "is larger than 1 MiB")
	}
	return data, nil
}

// holdsCertificates reports whether data has at least one CERTIFICATE
// block and every one of them parses.
func holdsCertificates(data []byte) bool {
	found := false
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return found
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return false
		}
		found = true
	}
}

// firstKeyBlock returns the key file's first PEM block, re-encoded alone,
// when it is an unencrypted private key (PKCS#1, PKCS#8 or SEC 1 EC). The
// first block, because that is the one pgx reads.
func firstKeyBlock(data []byte, field string) ([]byte, error) {
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, fieldErr(field, "is not a private key file")
	}
	if b.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(b.Headers["Proc-Type"], "ENCRYPTED") {
		return nil, fieldErr(field, "is an encrypted key, which is not supported; store it unencrypted with owner-only permissions")
	}
	if !strings.HasSuffix(b.Type, "PRIVATE KEY") || !parsesAsKey(b.Bytes) {
		return nil, fieldErr(field, "is not a private key file")
	}
	return pem.EncodeToMemory(b), nil
}

func parsesAsKey(der []byte) bool {
	if _, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return true
	}
	if _, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return true
	}
	_, err := x509.ParseECPrivateKey(der)
	return err == nil
}
