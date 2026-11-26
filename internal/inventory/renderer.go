package inventory

import (
	"cmp"
	"context"
	"maps"
	"slices"
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
	// carries names the rows a snapshot opens: render.Carries, the rows
	// the rendered document carries. Tests swap in every row as the
	// oracle the narrowing is checked against.
	carries func(render.Input) (render.Carried, error)
}

// NewRenderer wires the sources the renderer reads.
func NewRenderer(store Store, hostSource HostSource, ssh SSHSource, creds CredentialSource, box Sealer) *Renderer {
	return &Renderer{store: store, hosts: hostSource, ssh: ssh, creds: creds, box: box, carries: render.Carries}
}

// HostPreview is one host's rendered variables and connection. Connection is
// nil when the host is not rendered (it is not managed).
type HostPreview struct {
	Vars map[string]any
	// Sources says where each variable comes from: the name of the group
	// whose membership or schema gave it, or "host" for the host's own
	// (address, SSH settings, facts).
	Sources    map[string]string
	Connection *render.Connection
	Warnings   []render.Warning
	// AssistantWritten lists, sorted, the variables of Vars whose value an
	// assistant wrote and a run receives as plain text (decision D6).
	AssistantWritten []string
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
	out := HostPreview{Vars: map[string]any{}, Sources: map[string]string{}, Warnings: inv.Warnings}
	if hv, ok := inv.Doc["_meta"].(map[string]any)["hostvars"].(map[string]any)[name].(map[string]any); ok {
		// The effective values, lowest precedence first: the vars of the
		// groups the host is in (ancestors before descendants, then by
		// name, as Ansible merges them), then the host's own variables,
		// which include the membership values.
		written := map[render.AssistantValue]bool{}
		for _, w := range inv.AssistantWritten {
			written[w] = true
		}
		plain := map[string]bool{}
		for _, gv := range hostGroupVars(inv.Doc, name) {
			for k, v := range gv.vars {
				out.Vars[k], out.Sources[k] = v, gv.group
				plain[k] = written[render.AssistantValue{Group: gv.group, Key: k}]
			}
		}
		for k, v := range hv {
			out.Vars[k], out.Sources[k] = v, "host"
			plain[k] = false
			if g, ok := inv.Origins[name][k]; ok {
				out.Sources[k] = g
				plain[k] = written[render.AssistantValue{Group: g, Host: name, Key: k}]
			}
		}
		for _, k := range slices.Sorted(maps.Keys(plain)) {
			if plain[k] {
				out.AssistantWritten = append(out.AssistantWritten, k)
			}
		}
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
		in.Groups = append(in.Groups, render.Group{
			ID: g.ID, ParentID: g.ParentID, Name: g.Name, Schema: g.Schema, Vars: g.Vars, BastionID: g.BastionID,
			AssistantKeys: g.AssistantKeys, AssistantDefaultKeys: g.AssistantDefaultKeys,
		})
	}
	for _, m := range memberships {
		in.Memberships = append(in.Memberships, render.Membership{GroupID: m.GroupID, HostID: m.HostID, Vars: m.Vars, AssistantKeys: m.AssistantKeys})
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
	rows := varRows(orgID, &in, byID, hostNames)
	var revealed []string
	if reveal {
		revealed, err = r.reveal(in, rows)
	} else {
		err = mask(rows)
	}
	if err != nil {
		return render.Inventory{}, err
	}
	out, err := render.Render(in)
	if err != nil {
		return render.Inventory{}, err
	}
	out.Secrets = secretsInDoc(revealed, out.Doc)
	return out, nil
}

// secretsInDoc keeps the revealed secret values the rendered document
// actually carries (as a string value anywhere in it). A snapshot opens
// only the rows render.Carries names, but such a row can still hold a
// secret the document leaves out (an inactive field, a value whose shape
// no longer fits, see schema.Resolve), and a secret the run does not
// carry must not travel to the worker even as a redaction entry.
func secretsInDoc(revealed []string, doc map[string]any) []string {
	if len(revealed) == 0 {
		return nil
	}
	present := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			present[t] = true
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(doc)
	seen := map[string]bool{}
	var out []string
	for _, s := range revealed {
		if s != "" && present[s] && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// straySealedMessage builds reveal()'s ErrUnrenderable text from
// StraySealed's findings (backlog 2.22): a leaf stray (one scalar-typed
// field's own value is a sealed envelope) reads "the field is no longer a
// secret field", same as before this change; a container stray (the
// value sits where a whole object or repeater item was expected, or
// inside a structure no declared field explains) names its own path
// instead and says so — there is no longer a well-formed field at that
// path to call "no longer secret". Multiple strays of the same kind share
// one sentence; both kinds, when they occur together, get one sentence
// each.
func straySealedMessage(strays []schema.StrayValue) string {
	var leaves, containers []string
	for _, s := range strays {
		if s.Container {
			containers = append(containers, s.Path)
		} else {
			leaves = append(leaves, s.Path)
		}
	}
	var sentences []string
	if len(leaves) > 0 {
		sentences = append(sentences, strings.Join(leaves, ", ")+" still holds a stored secret, but the field is no longer a secret field; enter the value again")
	}
	if len(containers) > 0 {
		sentences = append(sentences, strings.Join(containers, ", ")+" holds a stored secret, but its container is no longer an object with a secret field; enter the value again")
	}
	return strings.Join(sentences, "; ")
}

type groupVars struct {
	group string
	vars  map[string]any
}

// hostGroupVars lists the vars of every group of the rendered document the
// host belongs to, directly or through a child group, in the order Ansible
// applies them: shallower groups first, then by name; a later entry wins.
func hostGroupVars(doc map[string]any, host string) []groupVars {
	entries := map[string]map[string]any{}
	parent := map[string][]string{}
	for name, e := range doc {
		m, ok := e.(map[string]any)
		if !ok || name == "_meta" || name == "all" || name == "ungrouped" {
			continue
		}
		entries[name] = m
		for _, c := range stringList(m["children"]) {
			parent[c] = append(parent[c], name)
		}
	}
	member := map[string]bool{}
	var mark func(g string)
	mark = func(g string) {
		if member[g] {
			return
		}
		member[g] = true
		for _, p := range parent[g] {
			mark(p)
		}
	}
	for name, m := range entries {
		if slices.Contains(stringList(m["hosts"]), host) {
			mark(name)
		}
	}
	var depth func(g string, seen map[string]bool) int
	depth = func(g string, seen map[string]bool) int {
		if seen[g] {
			return 0
		}
		seen[g] = true
		d := 0
		for _, p := range parent[g] {
			d = max(d, depth(p, seen)+1)
		}
		return d
	}
	var names []string
	for g := range member {
		names = append(names, g)
	}
	depths := map[string]int{}
	for _, g := range names {
		depths[g] = depth(g, map[string]bool{})
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(cmp.Compare(depths[a], depths[b]), cmp.Compare(a, b))
	})
	var out []groupVars
	for _, g := range names {
		if v, ok := entries[g]["vars"].(map[string]any); ok && len(v) > 0 {
			out = append(out, groupVars{group: g, vars: v})
		}
	}
	return out
}

func stringList(v any) []string {
	l, _ := v.([]string)
	return l
}
