// Package crypto wraps AES-256-GCM sealing with the instance master key.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// Box seals and opens secrets with one key.
type Box struct{ aead cipher.AEAD }

// NewBox builds an AES-256-GCM box.
func NewBox(key [32]byte) *Box {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // key length is fixed by the type
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &Box{aead: aead}
}

// Seal returns nonce || ciphertext.
func (b *Box) Seal(plain []byte) ([]byte, error) { return b.SealAAD(plain, nil) }

// Open reverses Seal and fails on any tampering.
func (b *Box) Open(sealed []byte) ([]byte, error) { return b.OpenAAD(sealed, nil) }

// SealAAD is Seal with additional authenticated data: the result only opens
// with the same aad. Callers pass a string naming the owning row (for example
// "credential:<org_id>:<id>"), so a ciphertext copied into another row or
// organization no longer opens.
func (b *Box) SealAAD(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, aad), nil
}

// OpenAAD reverses SealAAD with the same aad and fails on any tampering.
func (b *Box) OpenAAD(sealed, aad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], aad)
}
