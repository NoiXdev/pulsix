// The claim-time payload builders for the three kinds the worker runs
// self-contained (bootstrap, rotate_key, sync_repo): each resolves its
// references at claim time — the rendered route for the target, the
// credential secrets — into the kind payload the worker's executors
// consume, and the inventory document is discarded (nothing secret rides
// outside the payload and the claim response).
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// bootstrapRequest is the sealed payload the bootstrap API enqueues: which
// ssh_key credential's public key installs, and the operator-provided
// one-time login. The login is used once and never stored anywhere else.
type bootstrapRequest struct {
	CredentialID uuid.UUID          `json:"credential_id"`
	LoginUser    string             `json:"login_user"`
	Login        credentials.Secret `json:"login"`
}

// systemPayload resolves a bootstrap or rotate_key job's single target
// into the self-contained kind payload: the host object (address, port,
// the effective login user), its jump chain with each hop's credential
// inline, the pin, and the kind-specific secret material — the one-time
// login (bootstrap) or the old credential plus the pending new key
// (rotate_key).
// systemPayload resolves a bootstrap or rotate_key job's single target
// into the self-contained kind payload: the host object (address, port,
// login user), its jump chain with each hop's credential inline — the
// same shapes the connection-test payload uses.
func (q *SQLQueue) systemPayload(ctx context.Context, job Job) (string, claimPins, error) {
	if job.Kind == KindRotateKey {
		return q.rotateSystemPayload(ctx, job)
	}
	return q.bootstrapSystemPayload(ctx, job)
}

// bootstrapSystemPayload resolves the job's single host into the bootstrap
// payload: the one-time login (from the sealed enqueue payload) plus the
// host credential's public key and account.
func (q *SQLQueue) bootstrapSystemPayload(ctx context.Context, job Job) (string, claimPins, error) {
	if len(job.HostIDs) != 1 {
		return "", claimPins{}, fmt.Errorf("%w: bootstrap targets exactly one host, job %s carries %d", ErrInvalidInput, job.ID, len(job.HostIDs))
	}
	// The pin needs the hosts store: nil pins source is a wiring bug that
	// would silently run the bootstrap unpinned — fail closed.
	if q.pins == nil {
		return "", claimPins{}, fmt.Errorf("bootstrap claim: no host key pin source wired")
	}
	// A host whose key changed after the enqueue is not dialed.
	if _, err := q.excludeChangedAtClaim(ctx, job); err != nil {
		return "", claimPins{}, err
	}
	hostID := job.HostIDs[0]
	host, conn, hops, err := q.renderTargetConnection(ctx, job.OrgID, hostID)
	if err != nil {
		return "", claimPins{}, err
	}
	kindPayload, err := q.bootstrapKindPayload(ctx, job, host, hops)
	if err != nil {
		return "", claimPins{}, err
	}
	pins, err := q.claimHostKeys(ctx, job, map[string]render.Connection{host.Name: conn})
	if err != nil {
		return "", claimPins{}, err
	}
	return kindPayload, pins, nil
}

// rotateSystemPayload resolves the rotation job's target — a host through
// the rendered snapshot, a jump host through its own via-chain — and
// folds the old credential and the pending new key into the wire payload.
func (q *SQLQueue) rotateSystemPayload(ctx context.Context, job Job) (string, claimPins, error) {
	opened, err := q.openKindPayload(job)
	if err != nil {
		return "", claimPins{}, err
	}
	var rp rotationJobPayload
	if err := json.Unmarshal([]byte(opened), &rp); err != nil {
		return "", claimPins{}, fmt.Errorf("rotate_key %s: kind payload: %w", job.ID, err)
	}
	if rp.Target.Kind == rotationTargetBastion {
		return q.rotateBastionPayload(ctx, job, rp)
	}
	if len(job.HostIDs) != 1 {
		return "", claimPins{}, fmt.Errorf("%w: rotate_key targets exactly one host, job %s carries %d", ErrInvalidInput, job.ID, len(job.HostIDs))
	}
	if q.pins == nil {
		return "", claimPins{}, fmt.Errorf("rotate_key claim: no host key pin source wired")
	}
	hostID := job.HostIDs[0]
	if rp.Target.ID != hostID {
		return "", claimPins{}, fmt.Errorf("rotate_key %s: kind payload names %s, the job targets %s", job.ID, rp.Target.ID, hostID)
	}
	// A host whose key changed after the enqueue is not dialed.
	if _, err := q.excludeChangedAtClaim(ctx, job); err != nil {
		return "", claimPins{}, err
	}
	host, conn, hops, err := q.renderTargetConnection(ctx, job.OrgID, hostID)
	if err != nil {
		return "", claimPins{}, err
	}
	kindPayload, err := q.rotateKindPayload(ctx, job, rp, host, hops)
	if err != nil {
		return "", claimPins{}, err
	}
	pins, err := q.claimHostKeys(ctx, job, map[string]render.Connection{host.Name: conn})
	if err != nil {
		return "", claimPins{}, err
	}
	return kindPayload, pins, nil
}

// rotateBastionPayload resolves a jump host target: its own via-chain is
// walked in dial order (the outermost hop first), each hop's credential
// opened; the target itself logs in with the old credential. Bastions carry
// no pin (see BACKLOG: bastion host-key pinning), so the claim ships no host
// keys and the worker trusts the bastion's key on first use per run.
func (q *SQLQueue) rotateBastionPayload(ctx context.Context, job Job, rp rotationJobPayload) (string, claimPins, error) {
	target, err := q.bastions.GetBastion(ctx, job.OrgID, rp.Target.ID)
	if err != nil {
		return "", claimPins{}, fmt.Errorf("resolve rotation bastion %s: %w", rp.Target.ID, err)
	}
	// Walk the via-chain up from the target, bounded by a seen-set.
	var chain []sshconf.Bastion
	cur := target
	seen := map[uuid.UUID]bool{}
	for {
		if seen[cur.ID] {
			return "", claimPins{}, fmt.Errorf("rotation bastion %s: cycle in the jump chain", target.ID)
		}
		seen[cur.ID] = true
		chain = append(chain, cur)
		if cur.ViaBastionID == nil {
			break
		}
		next, err := q.bastions.GetBastion(ctx, job.OrgID, *cur.ViaBastionID)
		if err != nil {
			return "", claimPins{}, fmt.Errorf("resolve rotation bastion %s: resolve bastion %s: %w", target.ID, *cur.ViaBastionID, err)
		}
		cur = next
	}
	// chain[0] is the target, chain[1..] its hops in via-order; dial order
	// runs outermost-first, so the chain reverses and the target (the last
	// element afterwards) drops off the run's chain.
	slices.Reverse(chain)
	hops := make([]systemHop, 0, len(chain)-1)
	for _, b := range chain[:len(chain)-1] {
		hopCredential := ""
		if b.CredentialID != nil {
			secret, err := q.creds.OpenSecret(ctx, job.OrgID, *b.CredentialID)
			if err != nil {
				return "", claimPins{}, fmt.Errorf("open bastion credential %s: %w", *b.CredentialID, err)
			}
			if hopCredential, err = secretJSON(secret); err != nil {
				return "", claimPins{}, err
			}
		}
		hops = append(hops, systemHop{ID: b.ID, Address: b.Address, Port: b.Port, Username: b.Username, Credential: hopCredential})
	}
	host := systemHost{
		ID: target.ID, Name: target.Name, Address: target.Address,
		Port: target.Port, Username: target.Username,
	}
	kindPayload, err := q.rotateKindPayload(ctx, job, rp, host, hops)
	if err != nil {
		return "", claimPins{}, err
	}
	return kindPayload, claimPins{fingerprints: map[string]string{}, publicKeys: map[string]string{}}, nil
}

// renderTargetConnection resolves one host through the rendered snapshot:
// the host object, the snapshot connection, and its jump chain with each
// hop's credential inline.
func (q *SQLQueue) renderTargetConnection(ctx context.Context, orgID, hostID uuid.UUID) (systemHost, render.Connection, []systemHop, error) {
	inv, err := q.renderer.Snapshot(ctx, orgID, render.Scope{HostIDs: []uuid.UUID{hostID}})
	if err != nil {
		return systemHost{}, render.Connection{}, nil, fmt.Errorf("render connection: %w", err)
	}
	var conn render.Connection
	connName := ""
	for name, c := range inv.Connections {
		if c.HostID == hostID {
			conn, connName = c, name
			break
		}
	}
	if connName == "" {
		return systemHost{}, render.Connection{}, nil, fmt.Errorf("host %s renders no connection (not managed, or scope mismatch)", hostID)
	}
	host := systemHost{
		ID:      hostID,
		Name:    connName,
		Address: connAddress(inv.Doc, connName),
	}
	if host.Address == "" {
		host.Address = host.Name
	}
	host.Port, host.Username = connPortAndUser(inv.Doc, connName)

	// Every hop's credential is opened here; they travel only inside the
	// kind payload.
	hops := make([]systemHop, 0, len(conn.Bastions))
	for _, bastionID := range conn.Bastions {
		b, err := q.bastions.GetBastion(ctx, orgID, bastionID)
		if err != nil {
			return systemHost{}, render.Connection{}, nil, fmt.Errorf("resolve bastion %s: %w", bastionID, err)
		}
		hopCredential := ""
		if b.CredentialID != nil {
			secret, err := q.creds.OpenSecret(ctx, orgID, *b.CredentialID)
			if err != nil {
				return systemHost{}, render.Connection{}, nil, fmt.Errorf("open bastion credential %s: %w", *b.CredentialID, err)
			}
			if hopCredential, err = secretJSON(secret); err != nil {
				return systemHost{}, render.Connection{}, nil, err
			}
		}
		hops = append(hops, systemHop{ID: bastionID, Address: b.Address, Port: b.Port, Username: b.Username, Credential: hopCredential})
	}
	return host, conn, hops, nil
}

// bootstrapKindPayload folds the sealed enqueue payload's one-time login
// and the host credential's metadata into the wire kind payload.
func (q *SQLQueue) bootstrapKindPayload(ctx context.Context, job Job, host systemHost, hops []systemHop) (string, error) {
	opened, err := q.openKindPayload(job)
	if err != nil {
		return "", err
	}
	var req bootstrapRequest
	if err := json.Unmarshal([]byte(opened), &req); err != nil {
		return "", fmt.Errorf("%s %s: kind payload: %w", job.Kind, job.ID, err)
	}
	if req.Login.Password == "" && req.Login.PrivateKey == "" {
		return "", fmt.Errorf("bootstrap %s: the job payload carries no one-time login", job.ID)
	}
	cred, err := q.creds.Get(ctx, job.OrgID, req.CredentialID)
	if err != nil {
		return "", fmt.Errorf("resolve bootstrap credential %s: %w", req.CredentialID, err)
	}
	if cred.Kind != credentials.KindSSHKey {
		return "", fmt.Errorf("bootstrap %s: credential %s is a %s, not an ssh_key credential", job.ID, req.CredentialID, cred.Kind)
	}
	pulsixUser := cred.Username
	if pulsixUser == "" {
		pulsixUser = credentials.DefaultLoginUser
	}
	loginJSON, err := secretJSON(req.Login)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"host": host, "login_user": req.LoginUser, "login": loginJSON,
		"public_key": cred.PublicKey, "pulsix_user": pulsixUser,
		"bastions": hops,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode bootstrap kind payload: %w", err)
	}
	return string(b), nil
}

// rotateKindPayload folds the sealed enqueue payload (the pending new key)
// and the old credential's opened secret into the wire kind payload. For a
// host target the connection resolves through the rendered snapshot; for a
// bastion target the jump host's own via-chain is walked, each hop's
// credential opened, and the target itself logs in with the old
// credential.
func (q *SQLQueue) rotateKindPayload(ctx context.Context, job Job, rp rotationJobPayload, host systemHost, hops []systemHop) (string, error) {
	old, err := q.creds.OpenSecret(ctx, job.OrgID, rp.CredentialID)
	if err != nil {
		return "", fmt.Errorf("open rotating credential %s: %w", rp.CredentialID, err)
	}
	oldJSON, err := secretJSON(old)
	if err != nil {
		return "", err
	}
	newJSON, err := secretJSON(rp.NewPrivateKey)
	if err != nil {
		return "", err
	}
	cred, err := q.creds.Get(ctx, job.OrgID, rp.CredentialID)
	if err != nil {
		return "", fmt.Errorf("resolve rotating credential %s: %w", rp.CredentialID, err)
	}
	// The account that owns the installed key: the credential's own
	// username (the login the old credential authenticates as), falling
	// back to the rendered connection's or the bastion's.
	username := cred.Username
	if username == "" {
		username = host.Username
	}
	payload := map[string]any{
		"host": host, "username": username,
		"old_credential": oldJSON, "new_public_key": rp.NewPublicKey,
		"new_fingerprint": rp.NewFingerprint, "new_key_type": rp.NewKeyType,
		"new_private_key": newJSON, "pulsix_user": username,
		"bastions": hops,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode rotate-key kind payload: %w", err)
	}
	return string(b), nil
}

// systemHost / systemHop are the kind payload's host and hop objects —
// the shapes the worker's bootstrap and rotate executors parse. ID keeps
// the uuid.UUID type (it marshals as its string form, what the worker
// parses into).
type systemHost struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	Port     int       `json:"port"`
	Username string    `json:"username"`
}

type systemHop struct {
	ID         uuid.UUID `json:"id"`
	Address    string    `json:"address"`
	Port       int       `json:"port"`
	Username   string    `json:"username"`
	Credential string    `json:"credential"`
}

// syncRepoPayload completes the Task 6/9 contract for sync_repo claims:
// the stored {"repo_id"} resolves to the repository row (url, branch,
// subpath) and the repo's git credential opens into the credentials map as
// `git:<repo-id>` — exactly what the worker's sync executor fetches with.
// A nil repo source is a wiring bug and fails closed; an unknown
// repository fails the job (the sync cannot run without its repo).
func (q *SQLQueue) syncRepoPayload(ctx context.Context, job Job) (string, map[string]string, error) {
	if q.repos == nil {
		return "", nil, fmt.Errorf("sync_repo claim %s: no repo source wired", job.ID)
	}
	opened, err := q.openKindPayload(job)
	if err != nil {
		return "", nil, err
	}
	var sp syncPayload
	if err := json.Unmarshal([]byte(opened), &sp); err != nil || sp.RepoID == uuid.Nil {
		return "", nil, fmt.Errorf("sync_repo job %s: kind payload carries no repo id", job.ID)
	}
	repo, err := q.repos.Get(ctx, job.OrgID, sp.RepoID)
	if err != nil {
		return "", nil, fmt.Errorf("resolve repo %s: %w", sp.RepoID, err)
	}
	credentialsMap := map[string]string{}
	if repo.CredentialID != nil {
		secret, err := q.creds.OpenSecret(ctx, job.OrgID, *repo.CredentialID)
		if err != nil {
			return "", nil, fmt.Errorf("open repo credential %s: %w", *repo.CredentialID, err)
		}
		b, err := json.Marshal(secret)
		if err != nil {
			return "", nil, fmt.Errorf("encode repo credential %s: %w", *repo.CredentialID, err)
		}
		credentialsMap["git:"+sp.RepoID.String()] = string(b)
	}
	payload := map[string]any{"repo": map[string]any{
		"id": sp.RepoID.String(), "url": repo.URL, "branch": repo.Branch, "subpath": repo.Subpath,
	}}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("encode sync kind payload: %w", err)
	}
	return string(b), credentialsMap, nil
}
