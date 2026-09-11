// Package render turns an organization's inventory into an Ansible dynamic
// inventory (spec §6): groups with children, hosts and group vars; hostvars
// merged from each host's memberships; connection variables including the
// resolved jump host chain; host facts under pulsix_facts. It is pure: the
// caller passes every row in, decoded, with secrets already masked or
// revealed. The output is deterministic: every list is sorted.
package render

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/grouptree"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/sshconf"
)

// MaskedSecret replaces every secret value in a preview.
const MaskedSecret = "********"

// Where a host's jump host came from. A group source reads "group:<name>".
const (
	SourceHost = "host"
	SourceZone = "zone"
	SourceNone = "none"
)

// ErrUnknownGroup means Scope.GroupID names no group of the input.
var ErrUnknownGroup = errors.New("render: unknown group")

// ErrUnrenderable means the stored inventory cannot be rendered as it is (a
// jump host chain that is broken, a group tree with a cycle). The wrapping
// error names the host or group and the reason; neither is secret.
var ErrUnrenderable = errors.New("the inventory cannot be rendered")

// Group is one inventory group, vars decoded.
type Group struct {
	ID        uuid.UUID
	ParentID  *uuid.UUID
	Name      string
	Schema    schema.Schema
	Vars      map[string]any
	BastionID *uuid.UUID
}

// Membership is one host in one group, vars decoded.
type Membership struct {
	GroupID uuid.UUID
	HostID  uuid.UUID
	Vars    map[string]any
}

// Host is one host.
type Host struct {
	ID      uuid.UUID
	ZoneID  uuid.UUID
	Name    string
	Address string
	Managed bool
	Facts   map[string]any
}

// Scope selects hosts: none set renders every managed host; GroupID selects
// the members of that group and its descendants; HostIDs selects those
// hosts; both together intersect.
type Scope struct {
	GroupID *uuid.UUID
	HostIDs []uuid.UUID
}

// Input is everything a render needs.
type Input struct {
	Groups          []Group
	Memberships     []Membership
	Hosts           []Host
	SSH             map[uuid.UUID]sshconf.HostSSH // by host id; absent means defaults
	Bastions        map[uuid.UUID]sshconf.Bastion
	ZoneBastions    map[uuid.UUID]uuid.UUID
	CredentialUsers map[uuid.UUID]string // login user per credential id
	Scope           Scope
}

// Connection is how the worker will reach one host; the credential and jump
// hosts are ids, never secrets.
type Connection struct {
	HostID        uuid.UUID   `json:"host_id"`
	CredentialID  *uuid.UUID  `json:"credential_id,omitempty"`
	Bastions      []uuid.UUID `json:"bastions,omitempty"`
	BastionSource string      `json:"bastion_source"`
}

// Warning is something an operator should look at; it never blocks a render.
type Warning struct {
	Host    string `json:"host,omitempty"`
	Group   string `json:"group,omitempty"`
	Message string `json:"message"`
}

// Inventory is a rendered inventory. Doc is the dynamic-inventory JSON
// document (groups by name, "all", "ungrouped", "_meta.hostvars").
type Inventory struct {
	Doc         map[string]any
	Connections map[string]Connection
	Warnings    []Warning
}

type renderer struct {
	in       Input
	tree     grouptree.Tree
	groups   map[uuid.UUID]Group
	hosts    map[uuid.UUID]Host
	warnings []Warning
}

// Render renders in. It fails with ErrUnrenderable, rather than guessing,
// when a host's jump host cannot be resolved: a job must never take a
// different route than shown.
func Render(in Input) (Inventory, error) {
	r := renderer{in: in, groups: make(map[uuid.UUID]Group, len(in.Groups)), hosts: make(map[uuid.UUID]Host, len(in.Hosts))}
	nodes := make([]grouptree.Node, len(in.Groups))
	for i, g := range in.Groups {
		nodes[i] = grouptree.Node{ID: g.ID, ParentID: g.ParentID, Name: g.Name}
		r.groups[g.ID] = g
	}
	r.tree = grouptree.New(nodes)
	for _, h := range in.Hosts {
		r.hosts[h.ID] = h
	}
	return r.run()
}

func (r *renderer) wholeOrg() bool { return r.in.Scope.GroupID == nil && len(r.in.Scope.HostIDs) == 0 }

func (r *renderer) run() (Inventory, error) {
	selected, err := r.selectHosts()
	if err != nil {
		return Inventory{}, err
	}
	byHost := r.membershipsByHost(selected)
	hostvars := map[string]any{}
	conns := map[string]Connection{}
	var ungrouped []string
	for _, h := range r.sortedHosts(selected) {
		ms := byHost[h.ID]
		if len(ms) == 0 {
			ungrouped = append(ungrouped, h.Name)
		}
		vars, conn, err := r.hostVars(h, ms)
		if err != nil {
			return Inventory{}, err
		}
		hostvars[h.Name] = vars
		conns[h.Name] = conn
	}

	included := r.includedGroups(byHost)
	doc := map[string]any{}
	var roots []string
	for _, g := range r.sortedGroups() {
		if !included[g.ID] {
			continue
		}
		entry := map[string]any{}
		if vars := schema.Resolve(g.Schema.GroupFields(), g.Vars); len(vars) > 0 {
			entry["vars"] = vars
		}
		var children []string
		for _, c := range r.tree.Children(g.ID) {
			if included[c] {
				children = append(children, r.groups[c].Name)
			}
		}
		if len(children) > 0 {
			entry["children"] = children
		}
		var members []string
		for _, h := range r.sortedHosts(selected) {
			for _, m := range byHost[h.ID] {
				if m.GroupID == g.ID {
					members = append(members, h.Name)
				}
			}
		}
		if len(members) > 0 {
			entry["hosts"] = members
		}
		doc[g.Name] = entry
		if len(r.tree.Ancestors(g.ID)) == 0 {
			roots = append(roots, g.Name)
		}
	}
	allChildren := roots
	if len(ungrouped) > 0 {
		allChildren = append(allChildren, "ungrouped")
		doc["ungrouped"] = map[string]any{"hosts": ungrouped}
	}
	all := map[string]any{}
	if len(allChildren) > 0 {
		all["children"] = allChildren
	}
	doc["all"] = all
	doc["_meta"] = map[string]any{"hostvars": hostvars}

	slices.SortFunc(r.warnings, func(a, b Warning) int {
		return cmp.Or(cmp.Compare(a.Host, b.Host), cmp.Compare(a.Group, b.Group), cmp.Compare(a.Message, b.Message))
	})
	return Inventory{Doc: doc, Connections: conns, Warnings: r.warnings}, nil
}

func (r *renderer) selectHosts() (map[uuid.UUID]bool, error) {
	sel := map[uuid.UUID]bool{}
	for _, h := range r.in.Hosts {
		if h.Managed {
			sel[h.ID] = true
		}
	}
	if g := r.in.Scope.GroupID; g != nil {
		if !r.tree.Has(*g) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownGroup, *g)
		}
		subtree := map[uuid.UUID]bool{*g: true}
		for _, d := range r.tree.Descendants(*g) {
			subtree[d] = true
		}
		inSubtree := map[uuid.UUID]bool{}
		for _, m := range r.in.Memberships {
			if subtree[m.GroupID] {
				inSubtree[m.HostID] = true
			}
		}
		for id := range sel {
			if !inSubtree[id] {
				delete(sel, id)
			}
		}
	}
	if len(r.in.Scope.HostIDs) > 0 {
		want := map[uuid.UUID]bool{}
		for _, id := range r.in.Scope.HostIDs {
			want[id] = true
		}
		for id := range sel {
			if !want[id] {
				delete(sel, id)
			}
		}
	}
	return sel, nil
}

// membershipsByHost returns each selected host's memberships ordered by group
// depth (shallow first), then group name: the merge order of hostvars.
func (r *renderer) membershipsByHost(sel map[uuid.UUID]bool) map[uuid.UUID][]Membership {
	out := map[uuid.UUID][]Membership{}
	warned := map[uuid.UUID]bool{}

	// subtree, when Scope.GroupID is set, is that group and its
	// descendants — the same set selectHosts intersects managed hosts
	// against. hostWant, when Scope.HostIDs is set, is that set. A
	// membership is "in scope" — and so worth warning about, even though
	// its unmanaged host is never rendered — exactly when it would have
	// survived selectHosts's filtering had its host been managed.
	var subtree map[uuid.UUID]bool
	if g := r.in.Scope.GroupID; g != nil {
		subtree = map[uuid.UUID]bool{*g: true}
		for _, d := range r.tree.Descendants(*g) {
			subtree[d] = true
		}
	}
	hostWant := map[uuid.UUID]bool{}
	for _, id := range r.in.Scope.HostIDs {
		hostWant[id] = true
	}
	inScope := func(m Membership) bool {
		if subtree != nil && !subtree[m.GroupID] {
			return false
		}
		if len(hostWant) > 0 && !hostWant[m.HostID] {
			return false
		}
		return true
	}

	for _, m := range r.in.Memberships {
		h, ok := r.hosts[m.HostID]
		if _, known := r.groups[m.GroupID]; !ok || !known {
			continue
		}
		if !h.Managed {
			if inScope(m) && !warned[h.ID] {
				warned[h.ID] = true
				r.warnings = append(r.warnings, Warning{Host: h.Name, Message: "is a member of inventory groups but not managed; skipped"})
			}
			continue
		}
		if sel[m.HostID] {
			out[m.HostID] = append(out[m.HostID], m)
		}
	}
	for _, ms := range out {
		slices.SortFunc(ms, func(a, b Membership) int {
			return cmp.Or(cmp.Compare(r.tree.Depth(a.GroupID), r.tree.Depth(b.GroupID)), cmp.Compare(r.groups[a.GroupID].Name, r.groups[b.GroupID].Name))
		})
	}
	return out
}

func (r *renderer) includedGroups(byHost map[uuid.UUID][]Membership) map[uuid.UUID]bool {
	inc := map[uuid.UUID]bool{}
	if r.wholeOrg() {
		for id := range r.groups {
			inc[id] = true
		}
		return inc
	}
	for _, ms := range byHost {
		for _, m := range ms {
			inc[m.GroupID] = true
			for _, a := range r.tree.Ancestors(m.GroupID) {
				inc[a] = true
			}
		}
	}
	return inc
}

func (r *renderer) hostVars(h Host, ms []Membership) (map[string]any, Connection, error) {
	vars := map[string]any{}
	origin := map[string]string{}
	for _, m := range ms {
		g := r.groups[m.GroupID]
		resolved := schema.Resolve(g.Schema.MembershipFields(), m.Vars)
		keys := make([]string, 0, len(resolved))
		for k := range resolved {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			v := resolved[k]
			if prev, ok := vars[k]; ok && !reflect.DeepEqual(prev, v) {
				r.warnings = append(r.warnings, Warning{Host: h.Name, Group: g.Name,
					Message: fmt.Sprintf("%s is set by groups %s and %s; %s wins", k, origin[k], g.Name, g.Name)})
			}
			vars[k], origin[k] = v, g.Name
		}
	}

	ssh := r.sshFor(h.ID)
	host := h.Address
	if host == "" {
		host = h.Name
	}
	vars["ansible_host"] = host
	user := ssh.Username
	if user == "" && ssh.CredentialID != nil {
		user = r.in.CredentialUsers[*ssh.CredentialID]
	}
	if user != "" {
		vars["ansible_user"] = user
	}
	if ssh.Port != 0 && ssh.Port != sshconf.DefaultPort {
		vars["ansible_port"] = ssh.Port
	}
	hops, source, err := r.resolveBastion(h, ssh, ms)
	if err != nil {
		return nil, Connection{}, fmt.Errorf("%w: host %s: %w", ErrUnrenderable, h.Name, err)
	}
	conn := Connection{HostID: h.ID, CredentialID: ssh.CredentialID, BastionSource: source}
	if len(hops) > 0 {
		vars["ansible_ssh_common_args"] = "-o ProxyJump=" + sshconf.ProxyJump(hops)
		for _, b := range hops {
			conn.Bastions = append(conn.Bastions, b.ID)
		}
	}
	if len(h.Facts) > 0 {
		vars["pulsix_facts"] = h.Facts
	}
	return vars, conn, nil
}

func (r *renderer) sshFor(hostID uuid.UUID) sshconf.HostSSH {
	if s, ok := r.in.SSH[hostID]; ok {
		return s
	}
	return sshconf.HostSSH{HostID: hostID, Port: sshconf.DefaultPort, BastionMode: sshconf.ModeInherit}
}

func (r *renderer) sortedHosts(sel map[uuid.UUID]bool) []Host {
	out := make([]Host, 0, len(sel))
	for id := range sel {
		out = append(out, r.hosts[id])
	}
	slices.SortFunc(out, func(a, b Host) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

func (r *renderer) sortedGroups() []Group {
	out := slices.Clone(r.in.Groups)
	slices.SortFunc(out, func(a, b Group) int { return cmp.Compare(a.Name, b.Name) })
	return out
}
