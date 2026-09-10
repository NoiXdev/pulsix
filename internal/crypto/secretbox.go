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
func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open reverses Seal and fails on any tampering.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed value too short")
	}
	return b.aead.Open(nil, sealed[:n], sealed[n:], nil)
}
