package inventory

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// HostSource is the slice of hosts.Service rendering needs.
type HostSource interface {
	ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]hosts.Host, error)
}

// SSHSource is the slice of sshconf.Service rendering needs.
type SSHSource interface {
	ListHostSSH(ctx context.Context, orgID uuid.UUID) ([]sshconf.HostSSH, error)
	ListBastions(ctx context.Context, orgID uuid.UUID) ([]sshconf.Bastion, error)
	ZoneBastions(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]uuid.UUID, error)
}

// CredentialSource is the slice of credentials.Service rendering needs: the
// default login user of each credential, never its secret.
type CredentialSource interface {
	List(ctx context.Context, orgID uuid.UUID) ([]credentials.Credential, error)
}

// Renderer loads an organization's inventory and renders it.
type Renderer struct {
	store Store
	hosts HostSource
	ssh   SSHSource
	creds CredentialSource
	box   Sealer
}

// NewRenderer wires the sources the renderer reads.
func NewRenderer(store Store, hostSource HostSource, ssh SSHSource, creds CredentialSource, box Sealer) *Renderer {
	return &Renderer{store: store, hosts: hostSource, ssh: ssh, creds: creds, box: box}
}

// HostPreview is one host's rendered variables and connection. Connection is
// nil when the host is not rendered (it is not managed).
type HostPreview struct {
	Vars       map[string]any
	Connection *render.Connection
	Warnings   []render.Warning
}

// Preview renders scope with every secret replaced by render.MaskedSecret.
// It never opens a ciphertext.
func (r *Renderer) Preview(ctx context.Context, orgID uuid.UUID, scope render.Scope) (render.Inventory, error) {
	return r.render(ctx, orgID, scope, false)
}

// Snapshot renders scope with secrets decrypted, for the worker that runs a
// job (v0.2b). Its result must never be logged or returned by the public
// API.
func (r *Renderer) Snapshot(ctx context.Context, orgID uuid.UUID, scope render.Scope) (render.Inventory, error) {
	return r.render(ctx, orgID, scope, true)
}

// HostPreview renders one host with secrets masked.
func (r *Renderer) HostPreview(ctx context.Context, orgID, hostID uuid.UUID) (HostPreview, error) {
	all, err := r.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return HostPreview{}, err
	}
	var name string
	for _, h := range all {
		if h.ID == hostID {
			name = h.Name
		}
	}
	if name == "" {
		return HostPreview{}, ErrNotFound
	}
	inv, err := r.render(ctx, orgID, render.Scope{HostIDs: []uuid.UUID{hostID}}, false)
	if err != nil {
		return HostPreview{}, err
	}
	out := HostPreview{Vars: map[string]any{}, Warnings: inv.Warnings}
	if hv, ok := inv.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)[name].(map[string]any); ok {
		out.Vars = hv
	}
	if c, ok := inv.Connections[name]; ok {
		out.Connection = &c
	}
	return out, nil
}

func (r *Renderer) render(ctx context.Context, orgID uuid.UUID, scope render.Scope, reveal bool) (render.Inventory, error) {
	var out render.Inventory
	err := r.store.WithReadTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		out, err = r.renderIn(ctx, orgID, scope, reveal)
		return err
	})
	return out, err
}

// renderIn does the actual read-and-build work render used to do directly.
// It must run with ctx bound by Store.WithReadTx (backlog 2.16): every read
// below — groups and memberships from r.store, hosts from r.hosts, SSH
// settings and jump hosts from r.ssh, credential usernames from r.creds —
// goes through *db.DB underneath, no matter which package's store issued
// it, so binding ctx once here makes all of them share the one repeatable
// read transaction WithReadTx opened, instead of each seeing its own
// separate, possibly different, snapshot.
func (r *Renderer) renderIn(ctx context.Context, orgID uuid.UUID, scope render.Scope, reveal bool) (render.Inventory, error) {
	groups, err := r.store.ListGroups(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}
	byID := make(map[uuid.UUID]Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	if scope.GroupID != nil {
		if _, ok := byID[*scope.GroupID]; !ok {
			return render.Inventory{}, ErrNotFound
		}
	}
	memberships, err := r.store.ListMemberships(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}
	hostList, err := r.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}
	sshList, err := r.ssh.ListHostSSH(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}
	bastions, err := r.ssh.ListBastions(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}
	zoneBastions, err := r.ssh.ZoneBastions(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}
	creds, err := r.creds.List(ctx, orgID)
	if err != nil {
		return render.Inventory{}, err
	}

	in := render.Input{
		SSH: map[uuid.UUID]sshconf.HostSSH{}, Bastions: map[uuid.UUID]sshconf.Bastion{},
		ZoneBastions: zoneBastions, CredentialUsers: map[uuid.UUID]string{}, Scope: scope,
	}
	hostNames := make(map[uuid.UUID]string, len(hostList))
	for _, h := range hostList {
		hostNames[h.ID] = h.Name
	}
	for _, g := range groups {
		vars, err := r.secrets("inventory group "+g.Name, g.Schema.GroupFields(), g.Vars, GroupAAD(orgID, g.ID), reveal)
		if err != nil {
			return render.Inventory{}, err
		}
		in.Groups = append(in.Groups, render.Group{ID: g.ID, ParentID: g.ParentID, Name: g.Name, Schema: g.Schema, Vars: vars, BastionID: g.BastionID})
	}
	for _, m := range memberships {
		g := byID[m.GroupID]
		subject := "inventory group " + g.Name + ", host " + hostNames[m.HostID]
		vars, err := r.secrets(subject, g.Schema.MembershipFields(), m.Vars, MemberAAD(orgID, m.GroupID, m.HostID), reveal)
		if err != nil {
			return render.Inventory{}, err
		}
		in.Memberships = append(in.Memberships, render.Membership{GroupID: m.GroupID, HostID: m.HostID, Vars: vars})
	}
	for _, h := range hostList {
		in.Hosts = append(in.Hosts, render.Host{ID: h.ID, ZoneID: h.ZoneID, Name: h.Name, Address: h.Address, Managed: h.Managed, Facts: h.Facts})
	}
	for _, s := range sshList {
		in.SSH[s.HostID] = s
	}
	for _, b := range bastions {
		in.Bastions[b.ID] = b
	}
	for _, c := range creds {
		if c.Username != "" {
			in.CredentialUsers[c.ID] = c.Username
		}
	}
	return render.Render(in)
}

// secrets prepares one row's stored vars for rendering; subject names the
// row in errors. A preview masks every non-null value of a secret field and
// every other sealed value, declared or not, so no ciphertext envelope and
// no plaintext stored before a field became secret is ever shown. A
// snapshot opens the sealed values of secret fields and fails closed with
// ErrUnrenderable when a declared field that is no longer secret still
// holds a sealed value: Ansible must neither receive the envelope nor
// silently lose the value. Undeclared (orphaned) values stay sealed; the
// renderer drops them.
func (r *Renderer) secrets(subject string, fields []schema.Field, vars map[string]any, aad []byte, reveal bool) (map[string]any, error) {
	if !reveal {
		masked, err := schema.MapSecrets(fields, vars, func(string, any) (any, error) { return render.MaskedSecret, nil })
		if err != nil {
			return nil, err
		}
		return schema.ReplaceSealed(masked, func() any { return render.MaskedSecret }), nil
	}
	if stray := schema.StraySealed(fields, vars); len(stray) > 0 {
		return nil, fmt.Errorf("%w: %s: %s still holds a stored secret, but the field is no longer a secret field; enter the value again",
			ErrUnrenderable, subject, strings.Join(stray, ", "))
	}
	return schema.MapSecrets(fields, vars, func(_ string, v any) (any, error) {
		ct, ok := schema.SealedBytes(v)
		if !ok {
			return v, nil
		}
		plain, err := r.box.OpenAAD(ct, aad)
		if err != nil {
			return nil, fmt.Errorf("open sealed inventory value: %w", err)
		}
		return string(plain), nil
	})
}
