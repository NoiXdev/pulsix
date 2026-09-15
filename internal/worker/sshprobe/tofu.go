// Package sshprobe executes a managed host's connection test (spec §5,
// backlog 2.7c): it dials the host over SSH — through the host's jump chain
// when one resolves — captures the host's public-key fingerprint, compares
// it against the pinned key (trust on first use, fail closed on mismatch)
// and, only when the key check passes, authenticates and runs the
// well-known commands that decide passwordless sudo and Python presence.
//
// The package is worker-side and deliberately subprocess-free: the dial and
// the exec are plain golang.org/x/crypto/ssh calls, so tests drive an
// in-process SSH server instead of fixtures or real hosts.
package sshprobe

// KeyState is the TOFU verdict of one observation: how the host key that
// was just seen relates to the pin the claim carried.
type KeyState string

// The three verdicts the host_connection_tests row can store.
const (
	// KeyStateUnpinned: the host carries no pin, so nothing could mismatch
	// yet — the operator is expected to pin the observed key.
	KeyStateUnpinned KeyState = "unpinned"
	// KeyStatePinned: the observed key matches the pin.
	KeyStatePinned KeyState = "pinned"
	// KeyStateChanged: the observed key differs from the pin. The probe
	// aborts before authenticating — fail closed — and run jobs for the
	// host are blocked until an operator re-pins.
	KeyStateChanged KeyState = "changed"
)

// Compare is the pure trust-on-first-use decision: how does a key that was
// just observed relate to the pin it must match?
//
//   - no pin (pinned ""): unpinned, whatever was seen — there is nothing
//     to compare against, not even an empty observation.
//   - pin matches the observation: pinned.
//   - anything else: changed. That includes a pin against an empty
//     observation; callers pass a seen key only when the handshake actually
//     captured one, so the combination means the probe died mid-handshake
//     and there is nothing trustworthy to call this with.
func Compare(pinned, seen string) KeyState {
	switch pinned {
	case "":
		return KeyStateUnpinned
	case seen:
		return KeyStatePinned
	default:
		return KeyStateChanged
	}
}
