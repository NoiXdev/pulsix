package backup

import (
	"compress/gzip"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
)

// keyInfo labels the HKDF derivation. Changing it makes every existing
// backup undecryptable.
const keyInfo = "pulsix backup v1"

// headerKeyInfo labels the HKDF derivation of the header MAC key. Changing it
// makes every existing backup unverified.
const headerKeyInfo = "pulsix backup header v1"

// scryptWorkFactor is age's scrypt cost (log2 N). The passphrase is already
// a 256-bit derived key, so the work factor only needs to be cheap enough for
// a restore, not strong enough to slow guessing.
const scryptWorkFactor = 15

// DeriveKey returns the age passphrase for a master key: the lowercase hex of
// HKDF-SHA256(masterKey, no salt, "pulsix backup v1", 32 bytes).
func DeriveKey(masterKey [32]byte) string {
	okm, err := hkdf.Key(sha256.New, masterKey[:], nil, keyInfo, 32)
	if err != nil {
		// Only a malformed length or hash can fail, and both are constants.
		panic(fmt.Sprintf("derive backup key: %v", err))
	}
	return hex.EncodeToString(okm)
}

// HeaderKey returns the key that signs backup headers (SignHeader):
// HKDF-SHA256(masterKey, no salt, "pulsix backup header v1", 32 bytes),
// independent of the encryption key DeriveKey returns.
func HeaderKey(masterKey [32]byte) []byte {
	okm, err := hkdf.Key(sha256.New, masterKey[:], nil, headerKeyInfo, 32)
	if err != nil {
		// Only a malformed length or hash can fail, and both are constants.
		panic(fmt.Sprintf("derive backup header key: %v", err))
	}
	return okm
}

// Encrypt returns a writer that gzips what is written and encrypts the
// result to w with age, using DeriveKey(masterKey) as the scrypt passphrase.
// Close flushes the compressor and finishes the age stream; it does not
// close w.
func Encrypt(w io.Writer, masterKey [32]byte) (io.WriteCloser, error) {
	rcpt, err := age.NewScryptRecipient(DeriveKey(masterKey))
	if err != nil {
		return nil, fmt.Errorf("backup recipient: %w", err)
	}
	rcpt.SetWorkFactor(scryptWorkFactor)
	enc, err := age.Encrypt(w, rcpt)
	if err != nil {
		return nil, fmt.Errorf("start backup encryption: %w", err)
	}
	return &encryptWriter{gz: gzip.NewWriter(enc), enc: enc}, nil
}

type encryptWriter struct {
	gz  *gzip.Writer
	enc io.WriteCloser
}

func (e *encryptWriter) Write(p []byte) (int, error) { return e.gz.Write(p) }

func (e *encryptWriter) Close() error {
	gzErr := e.gz.Close()
	if err := e.enc.Close(); err != nil {
		return fmt.Errorf("finish backup encryption: %w", err)
	}
	if gzErr != nil {
		return fmt.Errorf("finish backup compression: %w", gzErr)
	}
	return nil
}

// Decrypt returns a reader over the decrypted, decompressed payload. A file
// encrypted under another master key fails with ErrWrongKey; a damaged or
// truncated one fails with ErrCorrupt, from Decrypt itself or from a later
// Read. Errors of r itself pass through unchanged. The payload is only
// authenticated once it has been read to io.EOF.
func Decrypt(r io.Reader, masterKey [32]byte) (io.Reader, error) {
	id, err := age.NewScryptIdentity(DeriveKey(masterKey))
	if err != nil {
		return nil, fmt.Errorf("backup identity: %w", err)
	}
	// A file we wrote never asks for more than scryptWorkFactor; refuse to
	// burn CPU on a header that does.
	id.SetMaxWorkFactor(scryptWorkFactor)

	src := &sourceTracker{r: r}
	dec, err := age.Decrypt(src, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, fmt.Errorf("%w: %w", ErrWrongKey, err)
		}
		return nil, src.classify(err)
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return nil, src.classify(err)
	}
	return &decryptReader{gz: gz, src: src}, nil
}

type decryptReader struct {
	gz  *gzip.Reader
	src *sourceTracker
}

func (d *decryptReader) Read(p []byte) (int, error) {
	n, err := d.gz.Read(p)
	if err != nil && err != io.EOF {
		err = d.src.classify(err)
	}
	return n, err
}

// sourceTracker remembers the last error of the underlying reader, so a
// failure of the file or network is not mistaken for corruption of the data.
type sourceTracker struct {
	r   io.Reader
	err error
}

func (s *sourceTracker) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

// classify returns err as is when the source reader caused it and as
// ErrCorrupt otherwise.
func (s *sourceTracker) classify(err error) error {
	if s.err != nil && errors.Is(err, s.err) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrCorrupt, err)
}
