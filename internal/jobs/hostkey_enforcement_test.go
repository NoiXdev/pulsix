// Host-key pins enforced on the connections that run playbooks (final
// review C2), the manager half: every host-bound claim that drives SSH —
// run, ad-hoc, bootstrap and rotate_key — ships the pinned hosts' full
// public keys (known_hosts) next to their fingerprints (host_keys); a pin
// that is only a fingerprint fails the claim closed with a clear reason;
// bootstrap and rotate_key claims refuse a host whose key changed; and the
// ad-hoc, bootstrap and rotation enqueues refuse such hosts up front.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

const (
	pinFP  = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	pinPub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPINNEDKEYPINNEDKEYPINNEDKEYPINNEDKEY"
)

// sealedPayload seals plain under the job's AAD in the stored form.
func sealedPayload(t *testing.T, q *SQLQueue, job Job, plain string) json.RawMessage {
	t.Helper()
	raw, err := q.box.SealAAD([]byte(plain), JobAAD(job.OrgID, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(`"` + base64.StdEncoding.EncodeToString(raw) + `"`)
}

func TestHostBoundClaimsShipKnownHostsForPins(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID := uuid.Must(uuid.NewV7())
	renderer := &scopeRecordingRenderer{all: map[string]render.Connection{"web1": {HostID: hostID, CredentialID: &credID}}}
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{credID: {PrivateKey: "old-key"}},
		metas:   map[uuid.UUID]credentials.Credential{credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible", PublicKey: "ssh-ed25519 AAAA cred"}},
	}
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookInline: "- hosts: all", Scope: json.RawMessage(`{}`)},
	}}

	jobFor := func(t *testing.T, q *SQLQueue, kind Kind) Job {
		j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: kind, HostIDs: []uuid.UUID{hostID}}
		switch kind {
		case KindRun:
			j.TemplateID = &templateID
		case KindAdhoc:
			j.Payload = json.RawMessage(`{"module":"shell","argv":"true"}`)
		case KindBootstrap:
			j.Payload = sealedPayload(t, q, j, `{"credential_id":"`+credID.String()+`","login_user":"root","login":{"password":"pw"}}`)
		case KindRotateKey:
			j.Payload = sealedPayload(t, q, j, `{"credential_id":"`+credID.String()+`","target":{"kind":"host","id":"`+hostID.String()+`"},"new_public_key":"ssh-ed25519 AAAA new"}`)
		}
		return j
	}

	for _, kind := range []Kind{KindRun, KindAdhoc, KindBootstrap, KindRotateKey} {
		t.Run(string(kind)+" ships the pinned key", func(t *testing.T) {
			q := newTestQueue(renderer, creds, templates, fakeBastions{})
			q.SetKeyPins(fakePins{byID: map[uuid.UUID]string{hostID: pinFP}, pubByID: map[uuid.UUID]string{hostID: pinPub}})
			out := &workerpb.Job{}
			if err := q.buildPayload(db.WithOrgScope(context.Background(), org), jobFor(t, q, kind), out); err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			if out.HostKeys["web1"] != pinFP || out.KnownHosts["web1"] != pinPub || len(out.KnownHosts) != 1 {
				t.Fatalf("host_keys = %v, known_hosts = %v; want the pin and its key for web1", out.HostKeys, out.KnownHosts)
			}
		})
		t.Run(string(kind)+" refuses a fingerprint-only pin", func(t *testing.T) {
			q := newTestQueue(renderer, creds, templates, fakeBastions{})
			q.SetKeyPins(fakePins{byID: map[uuid.UUID]string{hostID: pinFP}})
			err := q.buildPayload(db.WithOrgScope(context.Background(), org), jobFor(t, q, kind), &workerpb.Job{})
			if err == nil || !strings.Contains(err.Error(), "connection test") {
				t.Fatalf("buildPayload = %v, want a claim failure asking for a connection test", err)
			}
		})
		t.Run(string(kind)+" without pins ships no keys", func(t *testing.T) {
			q := newTestQueue(renderer, creds, templates, fakeBastions{})
			q.SetKeyPins(fakePins{})
			out := &workerpb.Job{}
			if err := q.buildPayload(db.WithOrgScope(context.Background(), org), jobFor(t, q, kind), out); err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			if len(out.HostKeys) != 0 || len(out.KnownHosts) != 0 {
				t.Fatalf("unpinned host shipped keys: %v / %v", out.HostKeys, out.KnownHosts)
			}
		})
	}
	for _, kind := range []Kind{KindBootstrap, KindRotateKey} {
		t.Run(string(kind)+" refuses a host whose key changed", func(t *testing.T) {
			q := newTestQueue(renderer, creds, templates, fakeBastions{})
			q.SetKeyPins(fakeChangedPins{changed: map[uuid.UUID]bool{hostID: true}})
			if err := q.buildPayload(db.WithOrgScope(context.Background(), org), jobFor(t, q, kind), &workerpb.Job{}); !errors.Is(err, ErrHostKeyChanged) {
				t.Fatalf("buildPayload = %v, want ErrHostKeyChanged", err)
			}
		})
	}
}

// guardHosts is the HostLookup + KeyGuardSource fake for the enqueue
// guards.
type guardHosts struct {
	byID    map[uuid.UUID]hosts.Host
	changed map[uuid.UUID]bool
}

func (g guardHosts) GetHost(_ context.Context, _, id uuid.UUID) (hosts.Host, error) {
	h, ok := g.byID[id]
	if !ok {
		return hosts.Host{}, hosts.ErrNotFound
	}
	return h, nil
}

func (g guardHosts) ChangedHosts(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		if g.changed[id] {
			out[id] = true
		}
	}
	return out, nil
}

func TestEnqueuesRefuseHostsWithAChangedKey(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	good, bad := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hs := guardHosts{
		byID: map[uuid.UUID]hosts.Host{
			good: {ID: good, OrgID: org, ZoneID: zone, Managed: true},
			bad:  {ID: bad, OrgID: org, ZoneID: zone, Managed: true},
		},
		changed: map[uuid.UUID]bool{bad: true},
	}
	newSvc := func() (*Service, *rotationOnlyStore) {
		store := newRotationOnlyStore()
		svc := NewService(store, noQueue{}, fakeTemplates{}, nil, hs, oneZone{zone}, fakeBox{}, time.Now, rand.Reader)
		return svc, store
	}
	scoped := db.WithOrgScope(context.Background(), org)

	cases := []struct {
		name    string
		enqueue func(svc *Service, hostIDs []uuid.UUID) error
		guarded bool
	}{
		{"ad-hoc", func(svc *Service, ids []uuid.UUID) error {
			_, err := svc.EnqueueAdhoc(scoped, org, ids, "shell", "true", nil)
			return err
		}, true},
		{"bootstrap", func(svc *Service, ids []uuid.UUID) error {
			_, err := svc.EnqueueHostJob(scoped, org, KindBootstrap, ids, json.RawMessage(`{}`), TriggerManual, nil)
			return err
		}, true},
		{"rotation", func(svc *Service, ids []uuid.UUID) error {
			targets := make([]RotationHost, len(ids))
			for i, id := range ids {
				targets[i] = RotationHost{ID: id, ZoneID: zone}
			}
			svc.rotationTargets = fakeRotationTargets{hosts: targets, bastions: []sshconf.Bastion{}}
			_, _, _, err := svc.EnqueueRotation(scoped, org, uuid.Must(uuid.NewV7()), credentials.PendingKey{PublicKey: "ssh-ed25519 AAAA new"}, nil)
			return err
		}, true},
		// The connection test is how a changed key gets re-verified: it is
		// never blocked by the guard.
		{"connection test", func(svc *Service, ids []uuid.UUID) error {
			_, err := svc.EnqueueHostJob(scoped, org, KindConnectionTest, ids, nil, TriggerManual, nil)
			return err
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := newSvc()
			err := c.enqueue(svc, []uuid.UUID{good, bad})
			if c.guarded {
				if !errors.Is(err, ErrHostKeyChanged) || !strings.Contains(err.Error(), bad.String()) {
					t.Fatalf("enqueue = %v, want ErrHostKeyChanged naming %s", err, bad)
				}
			} else if err != nil {
				t.Fatalf("enqueue = %v, want no guard", err)
			}
			svc2, _ := newSvc()
			if err := c.enqueue(svc2, []uuid.UUID{good}); err != nil {
				t.Fatalf("enqueue of a good host = %v", err)
			}
		})
	}
}
