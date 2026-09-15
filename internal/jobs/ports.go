// Narrow ports the two halves of the package depend on. They are declared
// here, next to their consumers, so unit tests drive fakes and only
// internal/app wires the real services (the same seam every domain package
// in Pulsix uses).
package jobs

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// GroupSource is the slice of inventory.Service the scope resolver needs:
// the groups with their parent links, so the group subtree of a scope can
// be walked.
type GroupSource interface {
	ListGroups(ctx context.Context, orgID uuid.UUID) ([]inventory.GroupSummary, error)
}

// MembershipSource is the slice of inventory.Service the scope resolver
// needs: every host-to-group membership of the organization.
type MembershipSource interface {
	ListMemberships(ctx context.Context, orgID uuid.UUID) ([]inventory.Membership, error)
}

// HostLister is the slice of hosts.Service the scope resolver and the host
// kinds need: every host of the organization (for zone grouping), and one
// host by id (for the existence/managed check).
type HostLister interface {
	ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]hosts.Host, error)
	GetHost(ctx context.Context, orgID, id uuid.UUID) (hosts.Host, error)
}

// DefaultZoneSource is the slice of hosts.Service the sync enqueue path
// needs: the org's zones, to find the "default" zone a sync_repo job runs
// in (hosts.Service satisfies it; unit tests use fakes).
type DefaultZoneSource interface {
	ListZones(ctx context.Context, orgID uuid.UUID) ([]hosts.Zone, error)
}

// SyncSink is the optional port a finished sync_repo job's terminal outcome
// flows through (SQLQueue.SetSyncSink): the concrete implementation is the
// org-scoped jobs.Service.RecordSyncResult, updating the repository's
// last_sync/last_sync_error. Declared here so the queue half never imports
// the service half.
type SyncSink interface {
	RecordSyncResult(ctx context.Context, orgID, repoID uuid.UUID, at time.Time, errText string) error
}

// TestSink is the optional port a finished connection_test job's outcome
// flows through (SQLQueue.SetTestSink): the concrete implementation is the
// org-scoped hosts.Service.RecordConnectionTest, appending the
// host_connection_tests row. Declared here so the queue half never imports
// the hosts service; checks are the manager-side mirror of the probe's
// check rows (hosts.HostTestCheck), parsed from the job's final event.
type TestSink interface {
	RecordConnectionTest(ctx context.Context, orgID, hostID uuid.UUID, at time.Time, ok bool, hostKey, hostKeyState string, checks []hosts.HostTestCheck) error
}

// KeyPinSource is the slice of hosts.Service the claim path needs: each
// host's pinned host-key fingerprint, absent for unpinned hosts. The claim
// turns the ids into the job's host_keys map (keyed by host name). A nil
// source makes every connection_test claim fail closed — the probe could
// not run trust on first use against the pin — while run and ad-hoc jobs
// simply ship no pins.
type KeyPinSource interface {
	PinnedKeys(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]string, error)
}

// SnapshotSource is the slice of inventory.Renderer the claim path needs:
// the secrets-revealed snapshot of the job's scope. Its result travels
// only inside the mTLS claim response and is never logged or persisted.
type SnapshotSource interface {
	Snapshot(ctx context.Context, orgID uuid.UUID, scope render.Scope) (render.Inventory, error)
}

// CredentialSource is the slice of credentials.Service the claim path
// needs: OpenSecret is the one read that returns plaintext material
// (backlog 2.17), Get reads the login-user metadata of the template's
// default credential. The claim path is the only caller of OpenSecret.
type CredentialSource interface {
	OpenSecret(ctx context.Context, orgID, id uuid.UUID) (credentials.Secret, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (credentials.Credential, error)
}

// BastionSource is the slice of sshconf.Service the claim path needs: the
// credential each bastion of a rendered jump chain logs in with.
type BastionSource interface {
	GetBastion(ctx context.Context, orgID, id uuid.UUID) (sshconf.Bastion, error)
}

// RepoSource is the slice of repos.Service the claim path needs for repo
// playbooks: the repository one resolves to — url, branch and subpath, plus
// the id of the git credential it logs in with. Ids and addresses only;
// the secret is opened separately into the credentials map.
type RepoSource interface {
	Get(ctx context.Context, orgID, repoID uuid.UUID) (RepoRef, error)
}

// RepoRef is the repository one repo playbook's claim carries to the
// worker: the fetch needs url, branch and the playbook's file path (the
// repo's own subpath joined with the playbook's path), and the git
// credential id opens the secret the claim ships as `git:<repo-id>`.
type RepoRef struct {
	ID           uuid.UUID
	URL          string
	Branch       string
	Subpath      string
	CredentialID *uuid.UUID
}
