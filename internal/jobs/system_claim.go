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
	"strings"

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
	switch job.Kind {
	case KindRotateKey:
		return q.rotateSystemPayload(ctx, job)
	case KindRevokeKey:
		return q.revokeSystemPayload(ctx, job)
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
	// The route's jump hosts: refuse a changed one, ship the pinned ones.
	if err := q.refuseChangedBastions(ctx, job.OrgID, conn.Bastions); err != nil {
		return "", claimPins{}, err
	}
	pins, err := q.claimRoutePins(ctx, job, map[string]render.Connection{host.Name: conn})
	if err != nil {
		return "", claimPins{}, err
	}
	kindPayload, err := q.bootstrapKindPayload(ctx, job, host, hops)
	if err != nil {
		return "", claimPins{}, err
	}
	return kindPayload, pins, nil
}

// rotateSystemPayload resolves the rotation job's target — a host through
// the rendered snapshot, a jump host through its own via-chain — and folds
// the old credential and the new PUBLIC key into the wire payload (the
// pending private key stays in the sealed job payload: the playbook run
// never needs it).
func (q *SQLQueue) rotateSystemPayload(ctx context.Context, job Job) (string, claimPins, error) {
	opened, err := q.openKindPayload(job)
	if err != nil {
		return "", claimPins{}, err
	}
	var rp rotationJobPayload
	if err := json.Unmarshal([]byte(opened), &rp); err != nil {
		return "", claimPins{}, fmt.Errorf("rotate_key %s: kind payload: %w", job.ID, err)
	}
	return q.credentialTargetPayload(ctx, job, rp.Target, func(host systemHost, hops []systemHop) (string, error) {
		return q.rotateKindPayload(ctx, job, rp, host, hops)
	})
}

// revokeSystemPayload resolves a revoke_key job's target exactly like its
// rotation's (a host through the rendered route, a jump host through its
// via-chain) and builds the revocation's kind payload.
func (q *SQLQueue) revokeSystemPayload(ctx context.Context, job Job) (string, claimPins, error) {
	opened, err := q.openKindPayload(job)
	if err != nil {
		return "", claimPins{}, err
	}
	var rp revocationJobPayload
	if err := json.Unmarshal([]byte(opened), &rp); err != nil || rp.CredentialID == uuid.Nil || rp.OldPublicKey == "" {
		return "", claimPins{}, fmt.Errorf("revoke_key %s: kind payload carries no credential or previous key", job.ID)
	}
	return q.credentialTargetPayload(ctx, job, rp.Target, func(host systemHost, hops []systemHop) (string, error) {
		return q.revokeKindPayload(ctx, job, rp, host, hops)
	})
}

// credentialTargetPayload resolves the target of a job acting on one
// credential's key (rotate_key, revoke_key): a jump host through its own
// via-chain (no pin), a host through the rendered route — refused when its
// key changed, with its pins shipped — and hands the resolved connection
// to build for the kind payload.
func (q *SQLQueue) credentialTargetPayload(ctx context.Context, job Job, target rotationTarget, build func(systemHost, []systemHop) (string, error)) (string, claimPins, error) {
	if target.Kind == rotationTargetBastion {
		return q.bastionTargetPayload(ctx, job, target.ID, build)
	}
	if len(job.HostIDs) != 1 {
		return "", claimPins{}, fmt.Errorf("%w: %s targets exactly one host, job %s carries %d", ErrInvalidInput, job.Kind, job.ID, len(job.HostIDs))
	}
	if q.pins == nil {
		return "", claimPins{}, fmt.Errorf("%s claim: no host key pin source wired", job.Kind)
	}
	hostID := job.HostIDs[0]
	if target.ID != hostID {
		return "", claimPins{}, fmt.Errorf("%s %s: kind payload names %s, the job targets %s", job.Kind, job.ID, target.ID, hostID)
	}
	// A host whose key changed after the enqueue is not dialed.
	if _, err := q.excludeChangedAtClaim(ctx, job); err != nil {
		return "", claimPins{}, err
	}
	host, conn, hops, err := q.renderTargetConnection(ctx, job.OrgID, hostID)
	if err != nil {
		return "", claimPins{}, err
	}
	// The route's jump hosts: refuse a changed one, ship the pinned ones.
	if err := q.refuseChangedBastions(ctx, job.OrgID, conn.Bastions); err != nil {
		return "", claimPins{}, err
	}
	kindPayload, err := build(host, hops)
	if err != nil {
		return "", claimPins{}, err
	}
	pins, err := q.claimRoutePins(ctx, job, map[string]render.Connection{host.Name: conn})
	if err != nil {
		return "", claimPins{}, err
	}
	return kindPayload, pins, nil
}

// revokeKindPayload builds a revocation's wire kind payload: the target,
// the credential's account, the credential's CURRENT secret as the login
// (the new key the finalize sealed), and the previous public key to remove.
func (q *SQLQueue) revokeKindPayload(ctx context.Context, job Job, rp revocationJobPayload, host systemHost, hops []systemHop) (string, error) {
	login, err := q.creds.OpenSecret(ctx, job.OrgID, rp.CredentialID)
	if err != nil {
		return "", fmt.Errorf("open rotated credential %s: %w", rp.CredentialID, err)
	}
	cred, err := q.creds.Get(ctx, job.OrgID, rp.CredentialID)
	if err != nil {
		return "", fmt.Errorf("resolve rotated credential %s: %w", rp.CredentialID, err)
	}
	// Never remove the key the credential logs in with now.
	if strings.TrimSpace(cred.PublicKey) == strings.TrimSpace(rp.OldPublicKey) {
		return "", fmt.Errorf("revoke_key %s: the previous key is the credential's current key; refusing to revoke it", job.ID)
	}
	loginJSON, err := secretJSON(login)
	if err != nil {
		return "", err
	}
	username := rotationAccount(host.Username, cred.Username)
	payload := map[string]any{
		"host": host, "username": username, "login_credential": loginJSON,
		"old_public_key": rp.OldPublicKey, "pulsix_user": username, "bastions": hops,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode revoke-key kind payload: %w", err)
	}
	return string(b), nil
}

// bastionTargetPayload resolves a jump host target: its own via-chain is
// walked in dial order (the outermost hop first), each hop's credential
// opened; build decides what the target itself logs in with. The target is
// dialed as the synthetic claim's host, so ITS OWN pin ships as the claim's
// host keys; the hops' pinned keys ship alongside (backlog 2.40). A route
// through a changed jump host — the hops or the target — is refused, the
// same block-on-mismatch a changed host key gets.
func (q *SQLQueue) bastionTargetPayload(ctx context.Context, job Job, bastionID uuid.UUID, build func(systemHost, []systemHop) (string, error)) (string, claimPins, error) {
	target, err := q.bastions.GetBastion(ctx, job.OrgID, bastionID)
	if err != nil {
		return "", claimPins{}, fmt.Errorf("resolve rotation bastion %s: %w", bastionID, err)
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
	hopIDs := make([]uuid.UUID, 0, len(chain))
	for _, b := range chain {
		hopIDs = append(hopIDs, b.ID)
	}
	// Every jump host the dial touches — the hops AND the target bastion —
	// is checked: a changed key fails the connection through it.
	if err := q.refuseChangedBastions(ctx, job.OrgID, hopIDs); err != nil {
		return "", claimPins{}, err
	}
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
	kindPayload, err := build(host, hops)
	if err != nil {
		return "", claimPins{}, err
	}
	// The target bastion is the dial's final step: its own pin ships as
	// the synthetic one-host claim's host keys, keyed by the target's name
	// (the synth claim's connection name).
	targetFPs, targetPubs, err := q.claimBastionKeys(ctx, job.OrgID, []uuid.UUID{target.ID})
	if err != nil {
		return "", claimPins{}, err
	}
	// The hops' pinned keys ride the bastion maps.
	hopsFPs, hopsPubs, err := q.claimBastionKeys(ctx, job.OrgID, hopIDs[:len(hopIDs)-1])
	if err != nil {
		return "", claimPins{}, err
	}
	pins := claimPins{
		fingerprints: map[string]string{}, publicKeys: map[string]string{},
		bastionFPs: hopsFPs, bastionPubs: hopsPubs,
	}
	if fp, ok := targetFPs[target.ID.String()]; ok {
		pins.fingerprints[target.Name] = fp
		pins.publicKeys[target.Name] = targetPubs[target.ID.String()]
	}
	return kindPayload, pins, nil
}

// refuseChangedBastions fails with ErrHostKeyChanged when any of the given
// jump hosts' LATEST hop observation reports a changed key (backlog 2.40,
// the bastion mirror of refuseChangedHosts). The guard is mandatory
// whenever the route names jump hosts — an unwired guard fails closed,
// exactly like the host key guard (excludeChangedAtClaim); the real
// wiring is hosts.Service, which always implements it. A guard read
// failure fails closed too.
func (q *SQLQueue) refuseChangedBastions(ctx context.Context, orgID uuid.UUID, bastionIDs []uuid.UUID) error {
	if len(bastionIDs) == 0 {
		return nil
	}
	if q.bastionGuard == nil {
		return fmt.Errorf("%w: no bastion key guard wired", ErrHostKeyChanged)
	}
	changed, err := q.bastionGuard.ChangedBastions(ctx, orgID, bastionIDs)
	if err != nil {
		return fmt.Errorf("resolve changed bastion keys: %w", err)
	}
	for _, id := range bastionIDs {
		if changed[id] {
			return fmt.Errorf("%w: jump host %s", ErrHostKeyChanged, id)
		}
	}
	return nil
}

// claimBastionKeys resolves the pinned jump hosts' fingerprints and full
// public keys (keyed by bastion id on the wire). A pinned bastion without
// its full key cannot be enforced — fail closed with the remedy.
func (q *SQLQueue) claimBastionKeys(ctx context.Context, orgID uuid.UUID, bastionIDs []uuid.UUID) (map[string]string, map[string]string, error) {
	fps, pubs := map[string]string{}, map[string]string{}
	if len(bastionIDs) == 0 || q.bastionPins == nil {
		return fps, pubs, nil
	}
	pinned, err := q.bastionPins.PinnedBastionKeys(ctx, orgID, bastionIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve bastion key pins: %w", err)
	}
	if len(pinned) == 0 {
		return fps, pubs, nil
	}
	pubsByID, err := q.bastionPins.PinnedBastionPublicKeys(ctx, orgID, bastionIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve pinned bastion public keys: %w", err)
	}
	for id, fingerprint := range pinned {
		pub := pubsByID[id]
		if pub == "" {
			return nil, nil, fmt.Errorf("jump host %s was pinned before full jump host keys were recorded, so its pin cannot be enforced: run a connection test routed through it (it completes the pin) and retry", id)
		}
		fps[id.String()] = fingerprint
		pubs[id.String()] = pub
	}
	return fps, pubs, nil
}

// claimRoutePins is claimHostKeys with the same name for the route-shaping
// callers: the rendered connections' host pins plus their routes' jump
// hosts.
func (q *SQLQueue) claimRoutePins(ctx context.Context, job Job, conns map[string]render.Connection) (claimPins, error) {
	return q.claimHostKeys(ctx, job, conns)
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

// rotateKindPayload folds the sealed enqueue payload's new public key and
// the old credential's opened secret (the login) into the wire kind
// payload. The pending new PRIVATE key is deliberately not shipped: the
// playbook installs a public key, and only the verification test (which
// authenticates with it) and the manager's finalize use the private key.
func (q *SQLQueue) rotateKindPayload(ctx context.Context, job Job, rp rotationJobPayload, host systemHost, hops []systemHop) (string, error) {
	old, err := q.creds.OpenSecret(ctx, job.OrgID, rp.CredentialID)
	if err != nil {
		return "", fmt.Errorf("open rotating credential %s: %w", rp.CredentialID, err)
	}
	oldJSON, err := secretJSON(old)
	if err != nil {
		return "", err
	}
	cred, err := q.creds.Get(ctx, job.OrgID, rp.CredentialID)
	if err != nil {
		return "", fmt.Errorf("resolve rotating credential %s: %w", rp.CredentialID, err)
	}
	username := rotationAccount(host.Username, cred.Username)
	payload := map[string]any{
		"host": host, "username": username,
		"old_credential": oldJSON, "new_public_key": rp.NewPublicKey,
		"new_fingerprint": rp.NewFingerprint, "new_key_type": rp.NewKeyType,
		"pulsix_user": username,
		"bastions":    hops,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode rotate-key kind payload: %w", err)
	}
	return string(b), nil
}

// rotationAccount is the account a rotation installs the new key for,
// verifies it as and removes the old one from: the one runs log in as
// (backlog 7.33). That is runUser, the target's own user — the rendered connection's user
// for a host target (its own SSH username, else the credential's), the
// bastion's username for a jump-host target — and the credential's own
// username only when that is empty. The rotation group used the
// credential's first, so on a host with its own SSH username the old key
// stayed on the account Pulsix actually logs in with, and the new one
// never reached it.
func rotationAccount(runUser, credUsername string) string {
	if runUser != "" {
		return runUser
	}
	return credUsername
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
	// The repository's git server key pin (backlog 2.41): a pinned repo's
	// fetch demands its pinned key (the worker pre-seeds the run's
	// known_hosts and connects strict), an unpinned one stays trust on
	// first use. Both ride the plain payload — a host public key is public.
	if repo.SSHHostKey != "" {
		payload["repo"].(map[string]any)["host_key"] = repo.SSHHostKey
		payload["repo"].(map[string]any)["host_public_key"] = repo.SSHHostPublicKey
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("encode sync kind payload: %w", err)
	}
	return string(b), credentialsMap, nil
}
