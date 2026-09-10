package crypto_test

import (
	"bytes"
	"testing"

	"github.com/noixdev/pulsix/internal/crypto"
)

func testKey() [32]byte {
	var key [32]byte
	copy(key[:], "0123456789abcdef0123456789abcdef")
	return key
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := crypto.NewBox(testKey())
	sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("ciphertext contains plaintext")
	}
	plain, err := box.Open(sealed)
	if err != nil || string(plain) != "hunter2" {
		t.Fatalf("open = %q, %v", plain, err)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

// TestSealIsRandomized asserts that sealing the same plaintext twice
// produces different ciphertexts, which only holds if Seal draws a fresh
// random nonce on every call.
func TestSealIsRandomized(t *testing.T) {
	box := crypto.NewBox(testKey())
	first, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same plaintext produced identical output; nonce is not fresh")
	}
}

// TestOpenRejectsTamperedNonce asserts that Open rejects tampering anywhere
// in the sealed value, including the nonce prefix, not only the ciphertext.
func TestOpenRejectsTamperedNonce(t *testing.T) {
	box := crypto.NewBox(testKey())
	sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[0] ^= 0xff
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("tampered nonce accepted")
	}
}

// TestOpenRejectsTruncated asserts that a sealed value shorter than the
// nonce size is rejected rather than panicking or slicing out of range.
func TestOpenRejectsTruncated(t *testing.T) {
	box := crypto.NewBox(testKey())
	sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Open(sealed[:len(sealed)-1]); err == nil {
		t.Fatal("truncated sealed value accepted")
	}
	if _, err := box.Open(nil); err == nil {
		t.Fatal("empty sealed value accepted")
	}
}

// TestOpenWithDifferentKeyFails asserts that a box sealed with one key
// cannot be opened with another, i.e. the key material is actually used.
func TestOpenWithDifferentKeyFails(t *testing.T) {
	box := crypto.NewBox(testKey())
	sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	var otherKey [32]byte
	copy(otherKey[:], "fedcba9876543210fedcba9876543210")
	other := crypto.NewBox(otherKey)
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("sealed value opened with wrong key")
	}
}
