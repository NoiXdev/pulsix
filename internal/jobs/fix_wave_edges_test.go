// Fail-closed edges of the final review fix wave: the revocation claim,
// the redact list, the pin and key-guard sources.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// erringPins fails one of its reads.
type erringPins struct {
	pins      map[uuid.UUID]string
	pinsErr   error
	pubErr    error
	changeErr error
}

func (e erringPins) PinnedKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return e.pins, e.pinsErr
}

func (e erringPins) PinnedPublicKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return nil, e.pubErr
}

func (e erringPins) ChangedHosts(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]bool, error) {
	return map[uuid.UUID]bool{}, e.changeErr
}

// pinsOnly pins without offering the full keys.
type pinsOnly struct{ pins map[uuid.UUID]string }

func (p pinsOnly) PinnedKeys(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]string, error) {
	return p.pins, nil
}

func TestClaimHostKeysFailsClosed(t *testing.T) {
	org, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	conns := map[string]render.Connection{"web1": {HostID: host}}
	job := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRun}
	pinned := map[uuid.UUID]string{host: "SHA256:x"}
	cases := map[string]KeyPinSource{
		"no pin source":        nil,
		"pin read fails":       erringPins{pinsErr: errors.New("db down")},
		"key read fails":       erringPins{pins: pinned, pubErr: errors.New("db down")},
		"no known-host source": pinsOnly{pins: pinned},
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			q := newTestQueue(fakeRenderer{}, fakeCreds{}, fakeTemplates{}, fakeBastions{})
			q.SetKeyPins(source)
			if _, err := q.claimHostKeys(context.Background(), job, conns); err == nil {
				t.Fatal("claimHostKeys must fail closed")
			}
		})
	}
}

func TestRevokeClaimFailsClosed(t *testing.T) {
	org, host, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	renderer := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"_meta": map[string]any{"hostvars": map[string]any{"web1": map[string]any{"ansible_host": "192.0.2.1"}}}},
		Connections: map[string]render.Connection{"web1": {HostID: host}},
	}}
	job := func(q *SQLQueue, payload string) Job {
		j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRevokeKey, HostIDs: []uuid.UUID{host}}
		j.Payload = sealedPayload(t, q, j, payload)
		return j
	}
	good := `{"credential_id":"` + credID.String() + `","target":{"kind":"host","id":"` + host.String() + `"},"old_public_key":"ssh-ed25519 AAAA old"}`
	cases := []struct {
		name    string
		creds   fakeCreds
		payload string
		want    string
	}{
		{"no previous key", fakeCreds{}, `{"credential_id":"` + credID.String() + `"}`, "previous key"},
		{"not json", fakeCreds{}, `{`, "previous key"},
		{"the previous key is the current key", fakeCreds{
			secrets: map[uuid.UUID]credentials.Secret{credID: {PrivateKey: "k"}},
			metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, PublicKey: "ssh-ed25519 AAAA old"}},
		}, good, "refusing to revoke"},
		{"credential secret unreadable", fakeCreds{err: errors.New("locked")}, good, "open rotated credential"},
		{"credential gone", fakeCreds{secrets: map[uuid.UUID]credentials.Secret{credID: {PrivateKey: "k"}}}, good, "resolve rotated credential"},
		{"target is another host", fakeCreds{}, `{"credential_id":"` + credID.String() + `","target":{"kind":"host","id":"` + uuid.Must(uuid.NewV7()).String() + `"},"old_public_key":"x"}`, "kind payload names"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newTestQueue(renderer, c.creds, fakeTemplates{}, fakeBastions{})
			q.SetKeyPins(fakePins{})
			err := q.buildPayload(db.WithOrgScope(context.Background(), org), job(q, c.payload), &workerpb.Job{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("buildPayload = %v, want %q", err, c.want)
			}
		})
	}

	// A jump-host target resolves through the bastion chain and ships no pins.
	bastionID := uuid.Must(uuid.NewV7())
	q := newTestQueue(renderer, fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {PrivateKey: "new-key"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Username: "ops", PublicKey: "ssh-ed25519 AAAA new"}},
	}, fakeTemplates{}, fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{bastionID: {ID: bastionID, Name: "jump", Address: "10.0.0.9", Port: 22}}})
	q.SetKeyPins(fakePins{})
	j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, Kind: KindRevokeKey, HostIDs: []uuid.UUID{}}
	j.Payload = sealedPayload(t, q, j, `{"credential_id":"`+credID.String()+`","target":{"kind":"bastion","id":"`+bastionID.String()+`"},"old_public_key":"ssh-ed25519 AAAA old"}`)
	out := &workerpb.Job{}
	if err := q.buildPayload(db.WithOrgScope(context.Background(), org), j, out); err != nil {
		t.Fatalf("bastion revocation: %v", err)
	}
	var payload struct {
		Host struct {
			Address string `json:"address"`
		} `json:"host"`
		OldPublicKey string `json:"old_public_key"`
	}
	if err := json.Unmarshal([]byte(out.KindPayload), &payload); err != nil || payload.Host.Address != "10.0.0.9" || payload.OldPublicKey != "ssh-ed25519 AAAA old" || len(out.HostKeys) != 0 {
		t.Fatalf("bastion revocation payload = %s (%v), pins %v", out.KindPayload, err, out.HostKeys)
	}
	if len(out.Redact) == 0 || out.Redact[0] != "new-key" {
		t.Fatalf("redact = %q, want the login key", out.Redact)
	}
}

func TestRedactSetEdges(t *testing.T) {
	var r redactSet
	r.addSecretJSON("")
	r.addSecretJSON("not-json-secret")
	r.addSecretJSON(`{"password":"pw-1234"}`)
	r.add("pw-1234", "")
	r.addKindPayload("")
	r.addKindPayload("{")
	r.addKindPayload(`{"bastions":[{"credential":"{\"token\":\"tok-5678\"}"}],"login":"raw-login"}`)
	want := []string{"not-json-secret", "pw-1234", "tok-5678", "raw-login"}
	if strings.Join(r.list, ",") != strings.Join(want, ",") {
		t.Fatalf("redact list = %q, want %q", r.list, want)
	}
	if err := r.addExtraVarSecrets(json.RawMessage(`{bad`), json.RawMessage(`{}`)); err == nil {
		t.Fatal("a broken schema must fail")
	}
	if err := r.addExtraVarSecrets(json.RawMessage(`{"fields":[]}`), json.RawMessage(`[1]`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-object extra vars = %v", err)
	}
	if err := r.addExtraVarSecrets(json.RawMessage(`null`), json.RawMessage(`{"a":"b"}`)); err != nil {
		t.Fatalf("no schema: %v", err)
	}
}

func TestRefuseChangedHostsGuardFailure(t *testing.T) {
	s := &Service{keyGuard: erringPins{changeErr: errors.New("db down")}}
	if err := s.refuseChangedHosts(context.Background(), uuid.Must(uuid.NewV7()), []uuid.UUID{uuid.Must(uuid.NewV7())}); err == nil {
		t.Fatal("a failing guard must refuse the enqueue")
	}
	if err := (&Service{}).refuseChangedHosts(context.Background(), uuid.Must(uuid.NewV7()), []uuid.UUID{uuid.Must(uuid.NewV7())}); err != nil {
		t.Fatalf("no guard wired: %v", err)
	}
}
