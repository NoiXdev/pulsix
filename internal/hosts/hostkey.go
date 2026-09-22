// Host-key pinning and connection-test history (spec §5, backlog 2.7c).
//
// A connection test's outcome lands here as a HostTest row; the operator
// pins the key a test observed (only what was seen — the API checks the
// pin against the latest result), and every later test compares its
// observation against the pin. A host whose latest test reports `changed`
// is blocked from jobs (the ChangedHosts read the jobs package consumes)
// until a fresh connection test no longer reports a changed key — re-pinning
// alone does not unblock it: the operator pins the newly observed key and
// the next test must confirm it (reporting `pinned`).
//
// A pin is the fingerprint plus the full public key: the fingerprint is
// what the probe compares, the key is what the worker writes into the
// run's known_hosts so ansible's own SSH connections enforce the pin.
package hosts

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// The TOFU verdicts a test row can carry, the stored spellings of the
// host_key_state column's CHECK constraint.
const (
	KeyStateUnpinned = "unpinned"
	KeyStatePinned   = "pinned"
	KeyStateChanged  = "changed"
)

// fingerprintPattern is the OpenSSH fingerprint shape SetHostKey accepts:
// `SHA256:` plus the unpadded base64 of the SHA-256 digest (43 characters).
var fingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// HostTestCheck is one probe stage's outcome, the manager-side mirror of
// the sshprobe Check row.
type HostTestCheck struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

// HostTest is one stored connection-test outcome.
type HostTest struct {
	ID     uuid.UUID `json:"id"`
	OrgID  uuid.UUID `json:"org_id"`
	HostID uuid.UUID `json:"host_id"`
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	// HostKey is the fingerprint the probe observed, empty when the
	// handshake never got that far.
	HostKey string `json:"host_key"`
	// HostPublicKey is the observed key itself in authorized-key form;
	// empty when nothing was observed, and on rows recorded before
	// migration 00021.
	HostPublicKey string `json:"host_public_key"`
	// HostKeyState is the TOFU verdict: unpinned, pinned or changed.
	HostKeyState string          `json:"host_key_state"`
	Checks       []HostTestCheck `json:"checks"`
}

// HostKeyPin is one host's pin: the OpenSSH fingerprint and the full
// public key in authorized-key form. PublicKey is empty for a pin made
// before full keys were stored (not enforceable until a connection test
// confirming the fingerprint fills it).
type HostKeyPin struct {
	Fingerprint string
	PublicKey   string
}

// Pin states (backlog 2.43), what the hosts read APIs report about a
// host's pin: full means the pin carries the full public key (the
// claimable, enforceable shape), fingerprint_only means it holds only the
// fingerprint — a pin made before migration 00021 — whose claims fail with
// the connection-test remedy until a test confirms the fingerprint and
// fills the key in.
const (
	PinStateFull            = "full"
	PinStateFingerprintOnly = "fingerprint_only"
)

// PinStates reports each given host's pin state. A host without a pin is
// absent from the map: unpinned is trust-on-first-use, not a pin state.
// Hosts of another organization are invisible, exactly like everywhere
// else in this package.
func (s *Service) PinStates(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	pins, err := s.store.ListPinnedKeys(ctx, orgID, hostIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(pins))
	for id, pin := range pins {
		if pin.PublicKey != "" {
			out[id] = PinStateFull
		} else {
			out[id] = PinStateFingerprintOnly
		}
	}
	return out, nil
}

// SetHostKey pins a host key: the fingerprint in the OpenSSH
// "SHA256:<base64>" shape and the key itself in authorized-key form, whose
// fingerprint must equal the given one. The host must exist in the
// organization (a foreign host is invisible, ErrNotFound). Creating the pin
// never requires a host_ssh row to exist: one is created with the defaults
// when the host carries no SSH settings of its own.
//
// The "you can only pin what you saw" rule — the pin must equal the latest
// test result's observation — is the API layer's check
// (PUT /hosts/{id}/host-key), which reads LatestTest before calling this
// and passes the key that test observed.
func (s *Service) SetHostKey(ctx context.Context, orgID, hostID uuid.UUID, fingerprint, publicKey string) error {
	if !fingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf("%w: host key fingerprint must match SHA256:<43 base64 characters>", ErrInvalidInput)
	}
	if err := checkPublicKey(fingerprint, publicKey); err != nil {
		return err
	}
	if _, err := s.store.GetHost(ctx, orgID, hostID); err != nil {
		return err
	}
	return s.store.SetHostKeyPin(ctx, orgID, hostID, fingerprint, publicKey)
}

// checkPublicKey verifies that publicKey is exactly one authorized-key
// line whose fingerprint is fingerprint.
func checkPublicKey(fingerprint, publicKey string) error {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("%w: host public key must be one authorized-key line", ErrInvalidInput)
	}
	if ssh.FingerprintSHA256(pub) != fingerprint {
		return fmt.Errorf("%w: host public key does not match the fingerprint", ErrInvalidInput)
	}
	return nil
}

// ClearHostKey removes the pin. A host without SSH settings of its own has
// no pin to clear — that is a no-op, not an error; the host itself must
// still exist.
func (s *Service) ClearHostKey(ctx context.Context, orgID, hostID uuid.UUID) error {
	if _, err := s.store.GetHost(ctx, orgID, hostID); err != nil {
		return err
	}
	return s.store.ClearHostKeyPin(ctx, orgID, hostID)
}

// LatestTest returns the newest stored connection-test outcome of the
// host, ErrNotFound when the host has never been tested.
func (s *Service) LatestTest(ctx context.Context, orgID, hostID uuid.UUID) (HostTest, error) {
	if _, err := s.store.GetHost(ctx, orgID, hostID); err != nil {
		return HostTest{}, err
	}
	return s.store.LatestHostTest(ctx, orgID, hostID)
}

// RecordConnectionTest appends one test outcome. It is the job queue's
// TestSink (SQLQueue.SetTestSink): the queue calls it on the finish path
// of a connection_test job, with explicit org and host ids and the probe
// result parsed from the job's final event. An empty host_key_state
// (nothing was observed) records as unpinned.
//
// A test that confirmed the pin (state pinned) and observed the full key
// completes a pin made before full keys were stored: the store fills the
// key only when the pin's fingerprint equals the observed key's.
func (s *Service) RecordConnectionTest(ctx context.Context, orgID, hostID uuid.UUID, at time.Time, ok bool, hostKey, hostPublicKey, hostKeyState string, checks []HostTestCheck) error {
	// The host must exist in the organization: a job id pointing at a
	// foreign host is a wiring bug, and the store-level foreign key alone
	// would turn it into an opaque database error.
	if _, err := s.store.GetHost(ctx, orgID, hostID); err != nil {
		return err
	}
	if hostKeyState == "" {
		hostKeyState = KeyStateUnpinned
	}
	// A key that does not match its own reported fingerprint is dropped
	// rather than stored: the row keeps what can be trusted.
	if hostPublicKey != "" && checkPublicKey(hostKey, hostPublicKey) != nil {
		hostPublicKey = ""
	}
	if err := s.store.CreateHostTest(ctx, HostTest{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, HostID: hostID, At: at,
		OK: ok, HostKey: hostKey, HostPublicKey: hostPublicKey, HostKeyState: hostKeyState, Checks: checks,
	}); err != nil {
		return err
	}
	if hostKeyState == KeyStatePinned && hostPublicKey != "" {
		return s.store.BackfillHostKeyPin(ctx, orgID, hostID, hostKey, hostPublicKey)
	}
	return nil
}

// ChangedHosts reports which of the given hosts' LATEST connection test
// reports a changed host key. The jobs package consults it in EnqueueRun's
// scope resolution: such hosts are excluded from the run scope (spec §5
// block-on-mismatch) until a later connection test no longer reports the
// change (re-pinning alone does not unblock the host).
func (s *Service) ChangedHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	ids, err := s.store.ListChangedKeys(ctx, orgID, hostIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// PinnedKeys maps each host id that carries a pin to its fingerprint.
// The job queue's claim path turns the ids into the claim's host_keys map
// (keyed by host name on the wire).
func (s *Service) PinnedKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	pins, err := s.store.ListPinnedKeys(ctx, orgID, hostIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(pins))
	for id, pin := range pins {
		out[id] = pin.Fingerprint
	}
	return out, nil
}

// PinnedPublicKeys maps each host id whose pin carries the full public key
// to that key (authorized-key form). A pinned host missing here has a
// fingerprint-only pin: the claim path refuses to run against it.
func (s *Service) PinnedPublicKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	pins, err := s.store.ListPinnedKeys(ctx, orgID, hostIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(pins))
	for id, pin := range pins {
		if pin.PublicKey != "" {
			out[id] = pin.PublicKey
		}
	}
	return out, nil
}

// ListOrgIDs returns every organization id — the daily re-test's
// iteration root. organizations carries no row-level security by design,
// and this read is the scheduler's, not a request's.
func (s *Service) ListOrgIDs(ctx context.Context) ([]uuid.UUID, error) {
	return s.store.ListOrgIDs(ctx)
}

// HostsDueForRetest returns the org's managed hosts with no connection
// test newer than since and none still queued — the re-test selector.
func (s *Service) HostsDueForRetest(ctx context.Context, orgID uuid.UUID, since time.Time) ([]Host, error) {
	return s.store.ListHostsDueForTest(ctx, orgID, since)
}
