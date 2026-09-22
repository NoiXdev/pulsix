// The sync sink's git server key pin (backlog 2.41): a repository's pin is
// the OpenSSH fingerprint of its git server's host key plus the full
// public key, written by RecordSyncResult from what the worker observed on
// the FIRST (unpinned) successful sync — trust on first use per
// repository. The stored pair is what the claim ships and the worker
// pre-seeds into a fetch's own known_hosts (StrictHostKeyChecking=yes), so
// a changed key fails every later fetch before any data is transferred.
//
// Threat model (spec §14's note for the pin tasks): the pin's write path is
// the worker's sync result event — non-secret host-key material over an
// authenticated, worker-bound channel. The manager stores a reported key
// only as a well-formed pair whose key carries exactly the reported
// fingerprint, and only while the repository is unpinned (the query's
// WHERE clause is the TOFU guard: a sink cannot overwrite a pin, a
// compromised worker cannot re-trust a changed server by itself).
package jobs

import (
	"bytes"
	"fmt"
	"regexp"

	"golang.org/x/crypto/ssh"
)

// repoFingerprintPattern is the OpenSSH fingerprint shape a repo pin
// accepts, the hosts/bastion shape: `SHA256:` plus the unpadded base64 of
// the SHA-256 digest (43 characters).
var repoFingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// checkRepoHostKey verifies that publicKey is exactly one authorized-key
// line whose fingerprint is fingerprint — the pair the sink may pin.
func checkRepoHostKey(fingerprint, publicKey string) error {
	if !repoFingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf("host key fingerprint must match SHA256:<43 base64 characters>")
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("host public key must be one authorized-key line")
	}
	if ssh.FingerprintSHA256(pub) != fingerprint {
		return fmt.Errorf("host public key does not match the fingerprint")
	}
	return nil
}
