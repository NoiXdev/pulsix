// The organization-level job settings (backlog 2.44) and the enqueue-time
// pin requirement they drive. The one setting today,
// jobs_require_pinned_hosts, is stored on the org-keyed org_job_settings
// table (migration 00033) and consulted by every enqueue path that would
// drive SSH — a template run's scope, an ad-hoc command's targets, a
// bootstrap's host and a rotation's host targets: when the setting is on,
// such an enqueue refuses hosts without a FULL pin (no pin at all, or a
// fingerprint-only pin whose key the worker's known_hosts could not
// enforce anyway), naming them in a 422. The connection test is how a host
// gets pinned and is never refused.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// jobSettingsStore is the store capability the org-level job settings
// need: read (ok false when no row is stored — the default-false
// semantics) and replace. The real SQLStore implements it on the
// org_job_settings table; a store that does not keeps the settings
// routes' service calls failing closed and the refusal off, exactly like
// an organization with the setting unset.
type jobSettingsStore interface {
	GetOrgJobSettings(ctx context.Context, orgID uuid.UUID) (OrgJobSettings, bool, error)
	PutOrgJobSettings(ctx context.Context, orgID uuid.UUID, st OrgJobSettings) error
}

// FullPinSource is the slice of hosts.Service the pin requirement needs:
// which of the given hosts carry a pin WITH its full public key. A host
// missing from the answer is unpinned or carries a fingerprint-only pin —
// refused either way while the requirement is on. Declared here, next to
// its consumer, so unit tests can drive fakes and internal/app wires the
// real hosts service (resolved by type in NewService, like keyGuard).
type FullPinSource interface {
	PinnedPublicKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error)
}

// OrgJobSettings is an organization's job settings. The zero value is what
// an organization without a stored row gets: everything off, so the
// spec's TOFU-for-one-job stays the default.
type OrgJobSettings struct {
	// JobsRequirePinnedHosts refuses enqueues whose targets are not fully
	// pinned (backlog 2.44). Off by default.
	JobsRequirePinnedHosts bool
}

// OrgJobSettings returns the organization's job settings (defaults when
// none are stored). A store without the settings capability is a wiring
// gap reported as invalid input, never as silently-off defaults.
func (s *Service) OrgJobSettings(ctx context.Context, orgID uuid.UUID) (OrgJobSettings, error) {
	if s.settings == nil {
		return OrgJobSettings{}, fmt.Errorf("%w: the store cannot carry the organization's job settings", ErrInvalidInput)
	}
	st, ok, err := s.settings.GetOrgJobSettings(ctx, orgID)
	if err != nil {
		return OrgJobSettings{}, fmt.Errorf("get org job settings: %w", err)
	}
	if !ok {
		return OrgJobSettings{}, nil
	}
	return st, nil
}

// SetOrgJobSettings replaces the organization's job settings. The only
// field today is a boolean, so there is nothing to validate beyond the
// store's own scoping.
func (s *Service) SetOrgJobSettings(ctx context.Context, orgID uuid.UUID, st OrgJobSettings) error {
	if s.settings == nil {
		return fmt.Errorf("%w: the store cannot carry the organization's job settings", ErrInvalidInput)
	}
	return s.settings.PutOrgJobSettings(ctx, orgID, st)
}

// refuseUnpinnedHosts fails the enqueue with ErrUnpinnedHosts, naming
// every affected host, when the organization requires pinned hosts and any
// of the targeted hosts carries no full pin. The checks a service runs
// with no settings source wired: nothing (the unwired setting has the
// default-off semantics, the same convention keyGuard follows). A settings
// read failure or a requirement whose pin source is unwired fails the
// enqueue closed — an unreadable requirement is never treated as
// silently off.
func (s *Service) refuseUnpinnedHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) error {
	if s.settings == nil {
		return nil
	}
	st, _, err := s.settings.GetOrgJobSettings(ctx, orgID)
	if err != nil {
		return fmt.Errorf("resolve org job settings: %w", err)
	}
	if !st.JobsRequirePinnedHosts {
		return nil
	}
	if s.fullPins == nil {
		return fmt.Errorf("%w: no pinned host key source wired", ErrUnpinnedHosts)
	}
	full, err := s.fullPins.PinnedPublicKeys(ctx, orgID, hostIDs)
	if err != nil {
		return fmt.Errorf("resolve pinned host keys: %w", err)
	}
	seen := map[uuid.UUID]bool{}
	var unpinned []string
	for _, id := range hostIDs {
		if full[id] != "" || seen[id] {
			continue
		}
		seen[id] = true
		unpinned = append(unpinned, "host "+id.String())
	}
	if len(unpinned) > 0 {
		return fmt.Errorf("%w: %s", ErrUnpinnedHosts, strings.Join(unpinned, ", "))
	}
	return nil
}

// ErrUnpinnedHosts means the organization requires pinned hosts
// (jobs_require_pinned_hosts, backlog 2.44) and the enqueue's targets
// include hosts without a full pin — no pin at all, or a fingerprint-only
// pin. The error names every affected host; a connection test is how a
// host gets pinned (or completes a fingerprint-only pin).
var ErrUnpinnedHosts = errors.New("the organization requires pinned hosts: run a connection test for each host and pin its key")
