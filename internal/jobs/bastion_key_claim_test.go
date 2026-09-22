// The bastion key pins' claim-side tests (backlog 2.40): the system kinds'
// claims ship the hops' pins, refuse routes through a changed jump host,
// and the rotation enqueue refuses a changed bastion target.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

const hopPinFP = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

const hopPinPub = "ssh-ed25519 AAAA"

// fullBastionPins shapes the fake pins source: the bastions' pinned
// fingerprints and their full public keys.
func fullBastionPins(fps, pubs map[uuid.UUID]string) fakePins {
	return fakePins{bastionByID: fps, bastionPubByID: pubs}
}

// bastionHopFixture builds the queue of a bootstrap claim for one host
// routed through one hop, with the key-pin source the tests shape.
func bastionHopFixture(t *testing.T, pins KeyPinSource) (*SQLQueue, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	org := uuid.Must(uuid.NewV7())
	hostID, credID, bastionID, hopCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10", "ansible_user": "ansible", "ansible_port": 2222},
		}}},
		Connections: map[string]render.Connection{
			"web1": {HostID: hostID, CredentialID: &credID, Bastions: []uuid.UUID{bastionID}, BastionSource: "host"},
		},
	}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:    {PrivateKey: "stored-host-key"},
			hopCredID: {Password: "hop-pw"},
		},
		metas: map[uuid.UUID]credentials.Credential{
			credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible", PublicKey: "ssh-ed25519 AAAA hostpub"},
		},
	}
	q := newTestQueue(renderer, creds, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		bastionID: {ID: bastionID, Address: "10.0.0.1", Port: 2223, Username: "jumper", CredentialID: &hopCredID},
	}})
	q.SetKeyPins(pins)
	return q, org, hostID, credID, bastionID
}

func bootstrapJob(t *testing.T, q *SQLQueue, org, credID uuid.UUID) Job {
	t.Helper()
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindBootstrap,
		HostIDs: []uuid.UUID{}}
	sealed, err := q.box.SealAAD([]byte(`{"credential_id":"`+credID.String()+`","login_user":"root","login":{"password":"one-time-pw"}}`), JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + sealedJSON(sealed) + `"`)
	return job
}

// TestBuildPayloadBootstrapShipsBastionPins: the bootstrap claim carries
// the pinned hop's fingerprint and full key.
func TestBuildPayloadBootstrapShipsBastionPins(t *testing.T) {
	// Shape the fake pins source for the fixture's own bastion id, which
	// the fixture hands back last.
	q, org, hostID, credID, bastionID := bastionHopFixture(t, fakePins{})
	q.SetKeyPins(fullBastionPins(
		map[uuid.UUID]string{bastionID: hopPinFP},
		map[uuid.UUID]string{bastionID: hopPinPub},
	))
	job := bootstrapJob(t, q, org, credID)
	job.HostIDs = []uuid.UUID{hostID}
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.BastionHostKeys[bastionID.String()] != hopPinFP {
		t.Fatalf("bastion_host_keys = %v", out.BastionHostKeys)
	}
	if out.BastionKnownHosts[bastionID.String()] != hopPinPub {
		t.Fatalf("bastion_known_hosts = %v", out.BastionKnownHosts)
	}
}

// TestBuildPayloadSystemKindRefusesChangedBastion: a route through a
// changed jump host is refused at claim time, like a changed host key.
func TestBuildPayloadSystemKindRefusesChangedBastion(t *testing.T) {
	q, org, hostID, credID, bastionID := bastionHopFixture(t, fakePins{})
	q.SetKeyPins(fakePins{changedBastions: map[uuid.UUID]bool{bastionID: true}})
	job := bootstrapJob(t, q, org, credID)
	job.HostIDs = []uuid.UUID{hostID}
	err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "host key changed") {
		t.Fatalf("a route through a changed bastion must be refused: %v", err)
	}
}

// TestBuildPayloadRotateBastionTargetShipsTargetPin: a bastion TARGET's own
// pin ships as the synthetic one-host claim's host keys (the dial's final
// step is the target bastion itself).
func TestBuildPayloadRotateBastionTargetShipsTargetPin(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	targetID := uuid.Must(uuid.NewV7())

	q := newTestQueue(fakeRenderer{}, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {Password: "old-password"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"}},
	}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		targetID: {ID: targetID, Name: "jump-01", Address: "10.0.0.9", Port: 2222, Username: "jumper", CredentialID: &credID},
	}})
	q.SetKeyPins(fullBastionPins(map[uuid.UUID]string{targetID: hopPinFP}, map[uuid.UUID]string{targetID: hopPinPub}))

	rp := rotationJobPayload{
		CredentialID:  credID,
		Target:        rotationTarget{Kind: rotationTargetBastion, ID: targetID},
		NewPublicKey:  "ssh-ed25519 AAAA new",
		NewPrivateKey: credentials.Secret{PrivateKey: "pending-new-key"},
	}
	raw, err := json.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRotateKey,
		HostIDs: []uuid.UUID{}}
	sealed, err := q.box.SealAAD(raw, JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + sealedJSON(sealed) + `"`)
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.HostKeys["jump-01"] != hopPinFP {
		t.Fatalf("host keys = %v, want the target bastion's fingerprint", out.HostKeys)
	}
	if out.KnownHosts["jump-01"] != hopPinPub {
		t.Fatalf("known hosts = %v, want the target bastion's key", out.KnownHosts)
	}
}

// TestEnqueueRotationRefusesChangedBastionTarget: a bastion target whose
// via-chain includes a changed jump host refuses the rotation.
func TestEnqueueRotationRefusesChangedBastion(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	zoneDefault := uuid.Must(uuid.NewV7())
	credID := uuid.Must(uuid.NewV7())
	targetID, outerID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	store := newRotationOnlyStore()
	svc := &Service{
		store: store, queue: noQueue{},
		zones: oneZone{zoneDefault},
		box:   fakeBox{}, now: time.Now, rand: rand.Reader,
		// The bastion guard: the OUTER hop's key changed.
		bastionKeyGuard: fakePins{changedBastions: map[uuid.UUID]bool{outerID: true}},
	}
	svc.rotationTargets = fakeRotationTargets{
		bastions: []sshconf.Bastion{{
			ID: targetID, Name: "jump-01", Address: "10.0.0.9", Port: 22, ViaBastionID: &outerID,
		}},
		// The org's full set: the outer hop is walked as a chain member
		// even though it does not reference the rotating credential.
		orgBastions: []sshconf.Bastion{
			{ID: targetID, Name: "jump-01", Address: "10.0.0.9", Port: 22, ViaBastionID: &outerID},
			{ID: outerID, Name: "edge-01", Address: "10.0.0.8", Port: 22},
		},
	}
	key := credentials.PendingKey{
		PrivateKey: credentials.Secret{PrivateKey: "new-pem"}, PublicKey: "ssh-ed25519 AAAA new",
		Fingerprint: "SHA256:new", KeyType: "ed25519",
	}
	_, _, _, err := svc.EnqueueRotation(db.WithOrgScope(context.Background(), org), org, credID, key, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "host key changed") {
		t.Fatalf("a rotation through a changed bastion must be refused: %v", err)
	}
	if len(store.jobs) != 0 {
		t.Fatalf("the refused rotation must insert nothing: %d jobs", len(store.jobs))
	}
}

// guardlessPins is a KeyPinSource fake that wires every key source EXCEPT
// the bastion guard — the wiring-gap shape the nil-guard tests drive.
type guardlessPins struct{}

func (guardlessPins) ChangedHosts(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, nil
}

func (guardlessPins) PinnedKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

func (guardlessPins) PinnedPublicKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

func (guardlessPins) PinnedBastionKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

func (guardlessPins) PinnedBastionPublicKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return map[uuid.UUID]string{}, nil
}

// TestNilBastionGuardFailsClaimsClosed pins the chosen nil-guard convention:
// the excludeChangedAtClaim one — the guard is mandatory for host-bound
// claims, so an unwired bastion guard fails the claim closed on BOTH
// host-bound paths: the system kinds' resolved hops and the generic path's
// rendered connections.
func TestNilBastionGuardFailsClaimsClosed(t *testing.T) {
	// The system path: the pins source wired, the bastion guard not.
	q, org, hostID, credID, _ := bastionHopFixture(t, guardlessPins{})
	job := bootstrapJob(t, q, org, credID)
	job.HostIDs = []uuid.UUID{hostID}
	err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{})
	if err == nil || !strings.Contains(err.Error(), "no bastion key guard wired") {
		t.Fatalf("a system claim without a bastion guard must fail closed: %v", err)
	}

	// The generic path: a run job whose queue never wired SetKeyPins (the
	// bastion guard stays nil), no bastions in the route — the guard is
	// still demanded, like the host key guard.
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{},
	}}
	creds := fakeCreds{secrets: map[uuid.UUID]credentials.Secret{}}
	q2 := &SQLQueue{
		box: fakeBox{}, renderer: renderer, creds: creds, templates: fakeTemplates{}, bastions: fakeBastions{},
		// Only the HOST key guard is wired; the bastion guard stays nil.
		keyGuard: fakePins{},
		now:      time.Now, rand: rand.Reader, streams: newStreamRegistry(), cancels: newCancelRegistry(),
	}
	templateID := uuid.Must(uuid.NewV7())
	q2.templates = fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {
			ID: templateID, OrgID: org, PlaybookID: uuid.Must(uuid.NewV7()),
			PlaybookInline: "- hosts: all\n  tasks: []\n",
			Scope:          json.RawMessage(`{}`),
		},
	}}
	job2 := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: uuid.Must(uuid.NewV7()), Kind: KindRun, TemplateID: &templateID, HostIDs: testHosts}
	err = q2.buildPayload(context.Background(), job2, &workerpb.Job{})
	if err == nil || !strings.Contains(err.Error(), "no bastion key guard wired") {
		t.Fatalf("a run claim without a bastion guard must fail closed: %v", err)
	}
}
