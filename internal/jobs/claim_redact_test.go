// The claim tells the worker what to redact (final review I4): every secret
// value a claim reveals travels in its redact list — credential secrets,
// secret-typed extra vars, secret inventory vars, one-time logins, the git
// credential — so the worker can remove them from run output before any
// event leaves it. Plain values are never listed.
package jobs

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/pb/workerpb"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// secretsRenderer renders one host and reports inventory secrets.
type secretsRenderer struct {
	hostID, credID uuid.UUID
	bastionID      *uuid.UUID
	secrets        []string
}

func (r secretsRenderer) Snapshot(context.Context, uuid.UUID, render.Scope) (render.Inventory, error) {
	conn := render.Connection{HostID: r.hostID, CredentialID: &r.credID, BastionSource: "none"}
	if r.bastionID != nil {
		conn.Bastions, conn.BastionSource = []uuid.UUID{*r.bastionID}, "host"
	}
	return render.Inventory{
		Doc: map[string]any{"all": map[string]any{}, "_meta": map[string]any{"hostvars": map[string]any{
			"web1": map[string]any{"ansible_host": "192.0.2.10", "ansible_user": "ansible", "vault_pass": "inventory-secret"},
		}}},
		Connections: map[string]render.Connection{"web1": conn},
		Secrets:     r.secrets,
	}, nil
}

func TestClaimsListEverySecretTheyReveal(t *testing.T) {
	org, zone := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	hostID, credID, hopCredID, bastionID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	templateID, repoID, gitCredID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	creds := fakeCreds{
		secrets: map[uuid.UUID]credentials.Secret{
			credID:    {PrivateKey: "-----BEGIN KEY-----\nhost-key-line\n-----END KEY-----\n", Password: "become-pw"},
			hopCredID: {Password: "hop-pw"},
			gitCredID: {Token: "git-token"},
		},
		metas: map[uuid.UUID]credentials.Credential{
			credID: {ID: credID, Kind: credentials.KindSSHKey, Username: "ansible", PublicKey: "ssh-ed25519 AAAA cur"},
		},
	}
	schemaJSON := `{"fields":[{"name":"db_pass","type":"secret"},{"name":"region","type":"string"}]}`
	templates := fakeTemplates{byID: map[uuid.UUID]TemplateRow{
		templateID: {ID: templateID, OrgID: org, PlaybookRepo: &repoID, PlaybookPath: "site.yml", Scope: json.RawMessage(`{}`),
			ExtraVars: json.RawMessage(`{"db_pass":"extra-var-secret","region":"eu-central"}`), ExtraVarsSchema: json.RawMessage(schemaJSON)},
	}}
	renderer := secretsRenderer{hostID: hostID, credID: credID, bastionID: &bastionID, secrets: []string{"inventory-secret"}}
	bastions := fakeBastions{byID: map[uuid.UUID]sshconf.Bastion{bastionID: {ID: bastionID, Address: "10.0.0.9", Port: 22, Username: "hop", CredentialID: &hopCredID}}}

	newQ := func() *SQLQueue {
		q := newTestQueue(renderer, creds, templates, bastions)
		q.SetKeyPins(fakePins{})
		q.SetRepoSource(fakeRepos{byID: map[uuid.UUID]RepoRef{repoID: {ID: repoID, URL: "ssh://git@example.org/x.git", Branch: "main", CredentialID: &gitCredID}}})
		return q
	}
	cases := []struct {
		name    string
		job     func(q *SQLQueue) Job
		want    []string
		notWant []string
	}{
		{
			name: "run",
			job: func(*SQLQueue) Job {
				return Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRun, TemplateID: &templateID, HostIDs: []uuid.UUID{hostID}}
			},
			want:    []string{"-----BEGIN KEY-----\nhost-key-line\n-----END KEY-----\n", "become-pw", "hop-pw", "git-token", "extra-var-secret", "inventory-secret"},
			notWant: []string{"eu-central", "ansible"},
		},
		{
			name: "bootstrap",
			job: func(q *SQLQueue) Job {
				j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindBootstrap, HostIDs: []uuid.UUID{hostID}}
				j.Payload = sealedPayload(t, q, j, `{"credential_id":"`+credID.String()+`","login_user":"root","login":{"password":"one-time-pw"}}`)
				return j
			},
			want:    []string{"one-time-pw", "hop-pw"},
			notWant: []string{"root"},
		},
		{
			name: "rotate_key",
			job: func(q *SQLQueue) Job {
				j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRotateKey, HostIDs: []uuid.UUID{hostID}}
				j.Payload = sealedPayload(t, q, j, `{"credential_id":"`+credID.String()+`","target":{"kind":"host","id":"`+hostID.String()+`"},"new_public_key":"ssh-ed25519 AAAA new","new_private_key":{"private_key":"pending-key"}}`)
				return j
			},
			want: []string{"become-pw", "-----BEGIN KEY-----\nhost-key-line\n-----END KEY-----\n", "hop-pw"},
		},
		{
			name: "revoke_key",
			job: func(q *SQLQueue) Job {
				j := Job{ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: zone, Kind: KindRevokeKey, HostIDs: []uuid.UUID{hostID}}
				j.Payload = sealedPayload(t, q, j, `{"credential_id":"`+credID.String()+`","target":{"kind":"host","id":"`+hostID.String()+`"},"old_public_key":"ssh-ed25519 AAAA old"}`)
				return j
			},
			want: []string{"become-pw", "hop-pw"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newQ()
			out := &workerpb.Job{}
			if err := q.buildPayload(db.WithOrgScope(context.Background(), org), c.job(q), out); err != nil {
				t.Fatalf("buildPayload: %v", err)
			}
			for _, w := range c.want {
				if !slices.Contains(out.Redact, w) {
					t.Errorf("redact list lacks %q: %q", w, out.Redact)
				}
			}
			for _, nw := range c.notWant {
				if slices.Contains(out.Redact, nw) {
					t.Errorf("redact list carries the plain value %q", nw)
				}
			}
		})
	}
}
