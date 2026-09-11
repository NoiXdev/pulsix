package credentials

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/ssh"
)

// minRSABits is the smallest RSA key accepted for import.
const minRSABits = 2048

type keyMaterial struct {
	privatePEM  string
	publicKey   string
	fingerprint string
	keyType     string
}

// generateKey creates a new ed25519 key from rand.
func generateKey(rand io.Reader) (keyMaterial, error) {
	_, priv, err := ed25519.GenerateKey(rand)
	if err != nil {
		return keyMaterial{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return fromPrivate(priv)
}

// importKey parses an OpenSSH or PEM private key, decrypting it with
// passphrase when it is protected, and re-encodes it unencrypted: the
// passphrase is used once and never stored.
func importKey(pemText, passphrase string) (keyMaterial, error) {
	data := []byte(pemText)
	raw, err := ssh.ParseRawPrivateKey(data)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if passphrase == "" {
			return keyMaterial{}, fmt.Errorf("%w: the private key is protected by a passphrase; supply it to import the key", ErrInvalidInput)
		}
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(passphrase))
	}
	if err != nil {
		return keyMaterial{}, fmt.Errorf("%w: the private key could not be read (wrong passphrase or unsupported format)", ErrInvalidInput)
	}
	if p, ok := raw.(*ed25519.PrivateKey); ok {
		raw = *p
	}
	return fromPrivate(raw)
}

func fromPrivate(raw any) (keyMaterial, error) {
	keyType, err := describeKey(raw)
	if err != nil {
		return keyMaterial{}, err
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return keyMaterial{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	block, err := ssh.MarshalPrivateKey(raw, "")
	if err != nil {
		return keyMaterial{}, fmt.Errorf("encode private key: %w", err)
	}
	pub := signer.PublicKey()
	return keyMaterial{
		privatePEM:  string(pem.EncodeToMemory(block)),
		publicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		fingerprint: ssh.FingerprintSHA256(pub),
		keyType:     keyType,
	}, nil
}

func describeKey(raw any) (string, error) {
	switch k := raw.(type) {
	case ed25519.PrivateKey:
		return "ed25519", nil
	case *rsa.PrivateKey:
		bits := k.N.BitLen()
		if bits < minRSABits {
			return "", fmt.Errorf("%w: RSA keys need at least %d bits", ErrInvalidInput, minRSABits)
		}
		return fmt.Sprintf("rsa-%d", bits), nil
	case *ecdsa.PrivateKey:
		return fmt.Sprintf("ecdsa-p%d", k.Curve.Params().BitSize), nil
	default:
		return "", fmt.Errorf("%w: unsupported key type; use ed25519, ECDSA or RSA with at least %d bits", ErrInvalidInput, minRSABits)
	}
}
