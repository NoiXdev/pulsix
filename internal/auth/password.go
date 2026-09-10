// Package auth implements passwords, sessions, TOTP, passkeys and API tokens.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32

	// maxArgonMemoryKiB bounds the memory parameter accepted from a stored
	// hash. It is far above what HashPassword ever produces (64 MiB) but
	// far below anything that could drive an out-of-control allocation if a
	// corrupted row or a future code path fed VerifyPassword a hostile
	// value.
	maxArgonMemoryKiB = 1 << 20 // 1 GiB
)

// HashPassword returns a PHC-formatted argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, enc(salt), enc(key)), nil
}

// VerifyPassword checks pw against a hash produced by HashPassword. hash is
// untrusted input — it may come straight from a database row that a bad
// migration or a corrupted record could have altered — so every parsed
// field is validated before it reaches argon2.IDKey. That function panics
// (or, for an out-of-range memory value, could attempt a huge allocation)
// on parameters that pass a purely structural parse, so VerifyPassword must
// reject those values itself rather than let them reach argon2.
func VerifyPassword(hash, pw string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported hash format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("parsing argon2 version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %d", version)
	}

	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, err
	}
	if t < 1 {
		return false, errors.New("invalid argon2 params: time must be at least 1")
	}
	if p < 1 {
		return false, errors.New("invalid argon2 params: parallelism must be at least 1")
	}
	if m == 0 || m > maxArgonMemoryKiB {
		return false, fmt.Errorf("invalid argon2 params: memory %d KiB out of range", m)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	if len(want) == 0 {
		return false, errors.New("invalid argon2 hash: empty key")
	}

	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
