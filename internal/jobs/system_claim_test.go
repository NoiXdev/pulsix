// The system kinds' claim enrichment (package jobs, white-box): bootstrap
// ships the one-time login plus the host credential's public key;
// rotate_key ships the old credential, the pending new key and the pinned
// fingerprint; sync_repo completes the Task 6/9 enrichment (repo row +
// git credential). Fakes from queue_payload_test.go.
package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

func TestBuildPayloadBootstrapShipsOneTimeLogin(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, bastionID, hopCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	pinned := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

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
	q.SetKeyPins(fakePins{byID: map[uuid.UUID]string{hostID: pinned}, pubByID: map[uuid.UUID]string{hostID: "ssh-ed25519 AAAA pinned"}})

	// The sealed enqueue payload the API built.
	req := bootstrapRequest{CredentialID: credID, LoginUser: "root", Login: credentials.Secret{Password: "one-time-pw"}}
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(`{"note":"ignored"}`)}
	sealed, err := q.box.SealAAD([]byte(`{"credential_id":"`+credID.String()+`","login_user":"root","login":{"password":"one-time-pw"}}`), JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + sealedJSON(sealed) + `"`)
	_ = req
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	if out.Kind != "bootstrap" || out.InventoryDoc != "{}" || len(out.Connections) != 0 || len(out.Credentials) != 0 {
		t.Fatalf("the bootstrap claim is self-contained: %+v", out)
	}
	if out.HostKeys["web1"] != pinned {
		t.Fatalf("host keys = %v", out.HostKeys)
	}
	var payload struct {
		Host struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Address  string `json:"address"`
			Port     int    `json:"port"`
			Username string `json:"username"`
		} `json:"host"`
		LoginUser  string `json:"login_user"`
		Login      string `json:"login"`
		PublicKey  string `json:"public_key"`
		PulsixUser string `json:"pulsix_user"`
		Bastions   []struct {
			Credential string `json:"credential"`
		} `json:"bastions"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatalf("kind payload = %q: %v", out.KindPayload, err)
	}
	if payload.LoginUser != "root" || payload.Login != `{"password":"one-time-pw"}` {
		t.Fatalf("one-time login = %q / %q", payload.LoginUser, payload.Login)
	}
	if payload.PublicKey != "ssh-ed25519 AAAA hostpub" || payload.PulsixUser != "ansible" {
		t.Fatalf("credential fields = %q / %q", payload.PublicKey, payload.PulsixUser)
	}
	if payload.Host.ID != hostID.String() || payload.Host.Address != "192.0.2.10" || payload.Host.Port != 2222 {
		t.Fatalf("host = %+v", payload.Host)
	}
	if len(payload.Bastions) != 1 || payload.Bastions[0].Credential != `{"password":"hop-pw"}` {
		t.Fatalf("hops = %+v", payload.Bastions)
	}
}

func TestBuildPayloadRotateShipsOldCredentialAndPendingKey(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10", "ansible_user": "ansible"},
		}}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID, CredentialID: &credID, BastionSource: "none"}},
	}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {Password: "old-password"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"}},
	}
	q := newTestQueue(renderer, creds, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})

	pending := credentials.Secret{PrivateKey: "pending-new-key"}
	rp := rotationJobPayload{
		CredentialID: credID,
		Target:       rotationTarget{Kind: rotationTargetHost, ID: hostID},
		NewPublicKey: "ssh-ed25519 AAAA new", NewFingerprint: "SHA256:new", NewKeyType: "ed25519",
		NewPrivateKey: pending,
	}
	raw, err := json.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID}}
	sealed, err := q.box.SealAAD(raw, JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + sealedJSON(sealed) + `"`)
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, out); err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	var payload struct {
		Username       string `json:"username"`
		OldCredential  string `json:"old_credential"`
		NewPublicKey   string `json:"new_public_key"`
		NewPrivateKey  string `json:"new_private_key"`
		NewFingerprint string `json:"new_fingerprint"`
		PulsixUser     string `json:"pulsix_user"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OldCredential != `{"password":"old-password"}` {
		t.Fatalf("old credential = %q", payload.OldCredential)
	}
	if payload.NewPrivateKey != `{"private_key":"pending-new-key"}` || payload.NewPublicKey != "ssh-ed25519 AAAA new" {
		t.Fatalf("pending key = %q / %q", payload.NewPrivateKey, payload.NewPublicKey)
	}
	if payload.Username != "ansible" || payload.PulsixUser != "ansible" || payload.NewFingerprint != "SHA256:new" {
		t.Fatalf("account fields = %+v", payload)
	}
}

// TestBuildPayloadRotateBastionTarget walks the via-chain: the target
// bastion logs in with the rotating credential, its own hops ride theirs.
func TestBuildPayloadRotateBastionTarget(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	credID, hopCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	targetID, outerID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	// The outer bastion (hop) is reached through nothing; the target logs
	// in with the rotating credential. Dial order: outer first.
	q := newTestQueue(fakeRenderer{}, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:    {Password: "old-password"},
			hopCredID: {Password: "outer-pw"},
		},
		metas: map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"}},
	}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		targetID: {ID: targetID, Name: "jump-01", Address: "10.0.0.9", Port: 2222, Username: "jumper", CredentialID: &credID},
		outerID:  {ID: outerID, Name: "edge-01", Address: "10.0.0.8", Port: 22, Username: "edge", CredentialID: &hopCredID},
	}})
	// Wire via: target ← outer.
	q.bastions = fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		targetID: {ID: targetID, Name: "jump-01", Address: "10.0.0.9", Port: 2222, Username: "jumper", CredentialID: &credID, ViaBastionID: &outerID},
		outerID:  {ID: outerID, Name: "edge-01", Address: "10.0.0.8", Port: 22, Username: "edge", CredentialID: &hopCredID},
	}}

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
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
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
	var payload struct {
		Host struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Address  string `json:"address"`
			Port     int    `json:"port"`
			Username string `json:"username"`
		} `json:"host"`
		Username      string `json:"username"`
		OldCredential string `json:"old_credential"`
		Bastions      []struct {
			ID         string `json:"id"`
			Address    string `json:"address"`
			Username   string `json:"username"`
			Credential string `json:"credential"`
		} `json:"bastions"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Host.ID != targetID.String() || payload.Host.Address != "10.0.0.9" || payload.Host.Port != 2222 {
		t.Fatalf("host = %+v", payload.Host)
	}
	if len(payload.Bastions) != 1 {
		t.Fatalf("hops = %+v, want the outer hop only", payload.Bastions)
	}
	hop := payload.Bastions[0]
	if hop.ID != outerID.String() || hop.Address != "10.0.0.8" || hop.Credential != `{"password":"outer-pw"}` {
		t.Fatalf("hop = %+v", hop)
	}
}

func TestBuildPayloadSystemKindsFailClosed(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID, CredentialID: &credID}},
	}}

	// A bootstrap claim without a pins source fails closed.
	q := newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap, HostIDs: []uuid.UUID{hostID}}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil || !strings.Contains(err.Error(), "no host key pin source wired") {
		t.Fatalf("missing pins source: %v", err)
	}

	// A bootstrap payload without a one-time login fails.
	q = newTestQueue(renderer, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job.ID = uuid.Must(uuid.NewV7())
	job.Payload = json.RawMessage(`{"credential_id":"` + credID.String() + `","login_user":"root"}`)
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil {
		t.Fatal("a payload without a one-time login must fail")
	}

	// A non-ssh_key bootstrap credential fails.
	q = newTestQueue(renderer, fakeCreds{
		metas: map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindGitToken, Username: "x"}},
	}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job.ID = uuid.Must(uuid.NewV7())
	sealed, err := q.box.SealAAD([]byte(`{"credential_id":"`+credID.String()+`","login_user":"root","login":{"password":"x"}}`), JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + sealedJSON(sealed) + `"`)
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil || !strings.Contains(err.Error(), "ssh_key") {
		t.Fatalf("non-ssh credential: %v", err)
	}

	// A rotate_key job whose payload names another host fails.
	q = newTestQueue(renderer, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {Password: "old"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible"}},
	}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	rp := rotationJobPayload{CredentialID: credID,
		Target: rotationTarget{Kind: rotationTargetHost, ID: uuid.Must(uuid.NewV7())}}
	raw, _ := json.Marshal(rp)
	job.Kind = KindRotateKey
	job.ID = uuid.Must(uuid.NewV7())
	sealed, err = q.box.SealAAD(raw, JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + sealedJSON(sealed) + `"`)
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil {
		t.Fatal("a payload naming another host must fail")
	}

	// A sync_repo claim without a repo source fails closed.
	q = newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	syncJob := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindSyncRepo,
		Payload: json.RawMessage(`{"repo_id":"` + uuid.Must(uuid.NewV7()).String() + `"}`)}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), syncJob, &workerpb.Job{}); err == nil || !strings.Contains(err.Error(), "no repo source wired") {
		t.Fatalf("missing repo source: %v", err)
	}

	// A sync_repo claim for an unknown repo fails.
	q = newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{}})
	syncJob.ID = uuid.Must(uuid.NewV7())
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), syncJob, &workerpb.Job{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown repo: %v", err)
	}
}

// sealedJSON encodes sealed ciphertext bytes as the stored jsonb string.
func sealedJSON(sealed []byte) string {
	return base64.StdEncoding.EncodeToString(sealed)
}

// TestBuildPayloadSystemClaimsFailClosedOnSources covers the claim-time
// source errors: a failing or missing renderer, an unopenable credential
// or bastion hop, a failing pins source and a broken git credential.
func TestBuildPayloadSystemClaimsFailClosedOnSources(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, hopID, hopCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID, CredentialID: &credID, Bastions: []uuid.UUID{hopID}}},
	}}
	hops := fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{
		hopID: {ID: hopID, Address: "10.0.0.1", Port: 22, CredentialID: &hopCredID},
	}}
	rotatePayload := rotationJobPayload{CredentialID: credID,
		Target:        rotationTarget{Kind: rotationTargetHost, ID: hostID},
		NewPublicKey:  "pub",
		NewPrivateKey: credentials.Secret{PrivateKey: "pem"}}
	bootstrapPayload := `{"credential_id":"` + credID.String() + `","login_user":"root","login":{"password":"pw"}}`

	newJob := func(kind Kind, payload string) Job {
		return Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: kind,
			HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(payload)}
	}

	// A failing renderer fails both kinds closed.
	q := newTestQueue(fakeRenderer{err: errors.New("render broken")}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), newJob(KindBootstrap, bootstrapPayload), &workerpb.Job{}); err == nil {
		t.Fatal("failing renderer must fail the bootstrap claim")
	}

	// An unopenable hop credential fails the claim.
	q = newTestQueue(renderer, fakeCreds{err: errors.New("locked")}, fakeTemplates{}, hops)
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), newJob(KindBootstrap, bootstrapPayload), &workerpb.Job{}); err == nil {
		t.Fatal("unopenable hop credential must fail the claim")
	}

	// An unknown bastion in the rendered chain fails the claim.
	q = newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), newJob(KindBootstrap, bootstrapPayload), &workerpb.Job{}); err == nil {
		t.Fatal("unknown bastion must fail the claim")
	}

	// A failing pins source fails the claim.
	q = newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{err: errors.New("locked")})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), newJob(KindBootstrap, bootstrapPayload), &workerpb.Job{}); err == nil {
		t.Fatal("failing pins source must fail the claim")
	}

	// A rotate claim whose credential cannot open fails.
	raw, err := json.Marshal(rotatePayload)
	if err != nil {
		t.Fatal(err)
	}
	q = newTestQueue(renderer, fakeCreds{err: errors.New("locked")}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job := newJob(KindRotateKey, string(raw))
	sealed, err := q.box.SealAAD(raw, JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil {
		t.Fatal("an unopenable rotating credential must fail the claim")
	}

	// A rotate claim whose credential metadata is gone fails.
	q = newTestQueue(renderer, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {Password: "old"}},
	}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	job.ID = uuid.Must(uuid.NewV7())
	sealed, err = q.box.SealAAD(raw, JobAAD(org, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{}); err == nil {
		t.Fatal("a missing rotating credential must fail the claim")
	}

	// A sync claim whose git credential cannot open fails.
	q = newTestQueue(fakeRenderer{}, fakeCreds{err: errors.New("locked")}, fakeTemplates{}, fakeBastions{})
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{
		uuid.Must(uuid.NewV7()): {ID: uuid.Must(uuid.NewV7()), URL: "https://x", Branch: "main", CredentialID: &credID},
	}})
	repoID := uuid.Must(uuid.NewV7())
	q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {ID: repoID, URL: "https://x", Branch: "main", CredentialID: &credID}}})
	syncJob := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindSyncRepo,
		Payload: json.RawMessage(`{"repo_id":"` + repoID.String() + `"}`)}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), syncJob, &workerpb.Job{}); err == nil {
		t.Fatal("an unopenable git credential must fail the sync claim")
	}
}

// TestBuildPayloadRotateBastionTargetFailClosed covers the bastion
// target's failure branches: an unknown target, a broken via-chain and an
// unopenable hop credential.
func TestBuildPayloadRotateBastionTargetFailClosed(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	credID, hopCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	targetID, outerID, missingID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	rotate := func(bastions map[uuid.UUID]sshconf.Bastion, creds CredentialSource, target rotationTarget) error {
		q := newTestQueue(fakeRenderer{}, creds, fakeTemplates{}, fakeBastions{byID: bastions})
		rp := rotationJobPayload{CredentialID: credID, Target: target,
			NewPublicKey: "pub", NewPrivateKey: credentials.Secret{PrivateKey: "pem"}}
		raw, err := json.Marshal(rp)
		if err != nil {
			t.Fatal(err)
		}
		job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey, HostIDs: []uuid.UUID{}}
		sealed, err := q.box.SealAAD(raw, JobAAD(org, job.ID))
		if err != nil {
			t.Fatal(err)
		}
		job.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealed) + `"`)
		return q.buildPayload(db.WithOrgScope(context.Background(), org), job, &workerpb.Job{})
	}

	// An unknown target bastion fails.
	if err := rotate(map[uuid.UUID]sshconf.Bastion{}, fullProbeCreds{},
		rotationTarget{Kind: rotationTargetBastion, ID: missingID}); err == nil {
		t.Fatal("an unknown target bastion must fail the claim")
	}

	// A broken via-chain (the hop id points nowhere) fails.
	if err := rotate(map[uuid.UUID]sshconf.Bastion{
		targetID: {ID: targetID, Address: "10.0.0.9", Port: 22, CredentialID: &credID, ViaBastionID: &missingID},
	}, fullProbeCreds{}, rotationTarget{Kind: rotationTargetBastion, ID: targetID}); err == nil {
		t.Fatal("a broken via-chain must fail the claim")
	}

	// A cycle in the via-chain fails the claim.
	if err := rotate(map[uuid.UUID]sshconf.Bastion{
		targetID: {ID: targetID, Address: "10.0.0.9", Port: 22, CredentialID: &credID, ViaBastionID: &outerID},
		outerID:  {ID: outerID, Address: "10.0.0.8", Port: 22, CredentialID: &hopCredID, ViaBastionID: &targetID},
	}, fullProbeCreds{}, rotationTarget{Kind: rotationTargetBastion, ID: targetID}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cyclic chain: %v", err)
	}

	// An unopenable hop credential fails the claim.
	if err := rotate(map[uuid.UUID]sshconf.Bastion{
		targetID: {ID: targetID, Address: "10.0.0.9", Port: 22, CredentialID: &credID, ViaBastionID: &outerID},
		outerID:  {ID: outerID, Address: "10.0.0.8", Port: 22, CredentialID: &hopCredID},
	}, fakeCreds{err: errors.New("locked")}, rotationTarget{Kind: rotationTargetBastion, ID: targetID}); err == nil {
		t.Fatal("an unopenable hop credential must fail the claim")
	}
}

// TestBuildPayloadSystemClaimsShapeValidation covers the target-shape and
// payload-corruption branches the happy paths skip.
func TestBuildPayloadSystemClaimsShapeAndPayloadValidation(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, host2, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID, CredentialID: &credID}},
	}}

	// A bootstrap job carrying two hosts is invalid input.
	q := newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap,
		HostIDs: []uuid.UUID{hostID, host2},
	}, &workerpb.Job{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("two-host bootstrap: %v", err)
	}

	// A rotate job carrying two hosts is invalid input.
	rotatePayload := rotationJobPayload{CredentialID: credID,
		Target:       rotationTarget{Kind: rotationTargetHost, ID: hostID},
		NewPublicKey: "pub", NewPrivateKey: credentials.Secret{PrivateKey: "pem"}}
	raw, err := json.Marshal(rotatePayload)
	if err != nil {
		t.Fatal(err)
	}
	q = newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID, host2},
		Payload: json.RawMessage(string(raw)),
	}, &workerpb.Job{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("two-host rotate: %v", err)
	}

	// A rotate payload that does not parse fails the claim.
	q = newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(`{"nope":1}`),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("a payload without a credential id must fail the rotate claim")
	}

	// A rotate claim without a pins source fails closed.
	q = newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(string(raw)),
	}, &workerpb.Job{}); err == nil || !strings.Contains(err.Error(), "no host key pin source wired") {
		t.Fatalf("missing pins source: %v", err)
	}

	// A bootstrap claim whose credential metadata is gone fails.
	bootstrapPayload := `{"credential_id":"` + credID.String() + `","login_user":"root","login":{"password":"pw"}}`
	q = newTestQueue(renderer, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(bootstrapPayload),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("a missing bootstrap credential must fail the claim")
	}

	// A bootstrap claim whose host does not render fails.
	q = newTestQueue(fakeRenderer{}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(bootstrapPayload),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("an unrenderable host must fail the claim")
	}

	// A rotate claim whose host does not render fails.
	q = newTestQueue(fakeRenderer{}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(string(raw)),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("an unrenderable rotation host must fail the claim")
	}

	// A sync claim whose payload carries no repo id fails.
	q = newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetRepoSource(fakeRepos{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindSyncRepo,
		Payload: json.RawMessage(`{"nope":1}`),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("a repo-less sync payload must fail the claim")
	}
}

// TestBuildPayloadSystemKindsEdgeShapes covers the remaining claim shapes:
// a rotate payload that is not a document, a wrong-AAD sealed payload, and
// the credential-username fallbacks both kinds take.
func TestBuildPayloadSystemKindsEdgeShapes(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	renderer := fakeRenderer{inv: render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10", "ansible_user": "ansible-rendered"},
		}}},
		Connections: map[string]render.Connection{"web1": {HostID: hostID, CredentialID: &credID}},
	}}

	// A rotate payload that is not a JSON object fails the unmarshal.
	q := newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(`[1,2]`),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("a non-document rotation payload must fail the claim")
	}

	// A sealed rotation payload that does not open under the job's AAD
	// fails closed.
	var aadKey [32]byte
	copy(aadKey[:], "jobs-system-wrong-aad-key-012")
	realBox := crypto.NewBox(aadKey)
	strangerSealed, sealErr3 := realBox.SealAAD([]byte(`{}`), JobAAD(org, uuid.Must(uuid.NewV7())))
	if sealErr3 != nil {
		t.Fatal(sealErr3)
	}
	q = newTestQueue(renderer, fullProbeCreds{}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	wrongAAD := *q
	wrongAAD.box = realBox
	if err := wrongAAD.buildPayload(db.WithOrgScope(context.Background(), org), Job{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID},
		Payload: json.RawMessage(`"` + base64.StdEncoding.EncodeToString(strangerSealed) + `"`),
	}, &workerpb.Job{}); err == nil {
		t.Fatal("a payload sealed under another AAD must fail the claim")
	}

	// The account fallback: a credential with no username takes the
	// rendered host's.
	rp := rotationJobPayload{CredentialID: credID,
		Target: rotationTarget{Kind: rotationTargetHost, ID: hostID},
		NewPublicKey: "pub", NewPrivateKey: credentials.Secret{PrivateKey: "pem"}}
	fallbackRaw, marshalErr := json.Marshal(rp)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	q = newTestQueue(renderer, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {Password: "old-pw"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey}},
	}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	fallbackJob := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey,
		HostIDs: []uuid.UUID{hostID}, Payload: json.RawMessage(string(fallbackRaw))}
	sealedFallback, sealErr2 := q.box.SealAAD(fallbackRaw, JobAAD(org, fallbackJob.ID))
	if sealErr2 != nil {
		t.Fatal(sealErr2)
	}
	fallbackJob.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealedFallback) + `"`)
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), fallbackJob, out); err != nil {
		t.Fatalf("rotate claim: %v", err)
	}
	var payload struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Username != "ansible-rendered" {
		t.Fatalf("fallback username = %q, want the rendered host user", payload.Username)
	}

	// The bootstrap's account fallback: an ssh_key credential with no
	// username installs for the default login user.
	bootstrapPayload := `{"credential_id":"` + credID.String() + `","login_user":"root","login":{"password":"pw"}}`
	q = newTestQueue(renderer, fakeCreds{
		metas: map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey, PublicKey: "pub"}},
	}, fakeTemplates{}, fakeBastions{})
	q.SetKeyPins(fakePins{})
	bootstrapJob := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap,
		HostIDs: []uuid.UUID{hostID}}
	sealedBootstrap, sealErr := q.box.SealAAD([]byte(bootstrapPayload), JobAAD(org, bootstrapJob.ID))
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	bootstrapJob.Payload = json.RawMessage(`"` + base64.StdEncoding.EncodeToString(sealedBootstrap) + `"`)
	out = &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), bootstrapJob, out); err != nil {
		t.Fatalf("bootstrap claim: %v", err)
	}
	var bootstrapWire struct {
		PulsixUser string `json:"pulsix_user"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &bootstrapWire); err != nil {
		t.Fatal(err)
	}
	if bootstrapWire.PulsixUser != credentials.DefaultLoginUser {
		t.Fatalf("fallback account = %q, want the default login user", bootstrapWire.PulsixUser)
	}
}
