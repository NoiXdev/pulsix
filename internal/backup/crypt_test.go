package backup

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"regexp"
	"testing"
)

var (
	keyOne = [32]byte{1}
	keyTwo = [32]byte{2}
)

func encryptBytes(t *testing.T, key [32]byte, plaintext []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := Encrypt(&buf, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plain := make([]byte, 5<<20)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}

	ct := encryptBytes(t, keyOne, plain)
	if bytes.Contains(ct, plain[:64]) {
		t.Fatal("ciphertext contains plaintext")
	}

	r, err := Decrypt(bytes.NewReader(ct), keyOne)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("decrypted bytes differ from the plaintext")
	}
}

func TestEncryptCompressesBeforeEncrypting(t *testing.T) {
	plain := bytes.Repeat([]byte("pulsix backup row\n"), 100_000)
	ct := encryptBytes(t, keyOne, plain)
	if len(ct) >= len(plain)/10 {
		t.Fatalf("ciphertext is %d bytes for %d compressible bytes", len(ct), len(plain))
	}
}

func TestDecryptWithOtherMasterKeyFails(t *testing.T) {
	ct := encryptBytes(t, keyOne, []byte("secret rows"))

	_, err := Decrypt(bytes.NewReader(ct), keyTwo)
	if !errors.Is(err, ErrWrongKey) {
		t.Fatalf("err = %v, want ErrWrongKey", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Fatalf("a wrong key must not read as corruption: %v", err)
	}
}

func TestDecryptTruncatedFails(t *testing.T) {
	plain := make([]byte, 1<<20)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	ct := encryptBytes(t, keyOne, plain)
	ct = ct[:len(ct)-100]

	r, err := Decrypt(bytes.NewReader(ct), keyOne)
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestDecryptFlippedByteFails(t *testing.T) {
	plain := make([]byte, 1<<20)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	ct := encryptBytes(t, keyOne, plain)
	ct[len(ct)/2] ^= 0xff

	r, err := Decrypt(bytes.NewReader(ct), keyOne)
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestDecryptRejectsNonAgeInput(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":   nil,
		"garbage": bytes.Repeat([]byte("not an age file "), 20),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decrypt(bytes.NewReader(in), keyOne)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestDecryptPassesSourceErrorsThrough(t *testing.T) {
	boom := errors.New("disk on fire")
	_, err := Decrypt(failingReader{boom}, keyOne)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the source error", err)
	}
	if errors.Is(err, ErrCorrupt) {
		t.Fatalf("an I/O failure must not read as corruption: %v", err)
	}
}

func TestDeriveKeyIsStableAndLabelled(t *testing.T) {
	got := DeriveKey(keyOne)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("DeriveKey = %q, want 64 lowercase hex chars", got)
	}
	if again := DeriveKey(keyOne); again != got {
		t.Errorf("DeriveKey is not stable: %q vs %q", got, again)
	}
	if other := DeriveKey(keyTwo); other == got {
		t.Error("different master keys derive the same backup key")
	}
	// HKDF-SHA256(ikm = 0x01 followed by 31 zero bytes, salt nil, info
	// "pulsix backup v1", 32 bytes), computed independently with HMAC. A
	// change of the label or the construction breaks every existing backup,
	// so it must break this test first.
	const golden = "8332e9b02b286cbae0ee6318069acc044eb1692638b58bef54c67404ccdf039a"
	if got != golden {
		t.Errorf("DeriveKey = %s, want %s", got, golden)
	}
}
