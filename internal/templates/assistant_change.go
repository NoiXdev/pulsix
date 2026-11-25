package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// SetChangeWatcher wires the watcher an assistant's run-relevant change is
// handed to (spec 2026-11-25 C2): it pauses what runs the template until a
// person reviews it. Without one nothing is handed on.
func (s *Service) SetChangeWatcher(w assistantchange.Watcher) { s.watcher = w }

// IDsOfRepo lists the templates whose playbook belongs to the repository:
// what a change of the repository changes for them (spec 2026-11-25 C2).
func (s *Service) IDsOfRepo(ctx context.Context, orgID, repoID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.IDsOfRepo(ctx, orgID, repoID)
}

// IDsReaching lists the templates of ids and every template that starts one
// of them through follow-ups, directly or along a chain: a run of any of
// them runs the changed template (final review 1).
func (s *Service) IDsReaching(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	return s.store.IDsReaching(ctx, orgID, ids)
}

// IDsOfPlaybook lists the templates that run the playbook: what a change of
// an editor playbook's text changes for them (spec 2026-11-25 C2).
func (s *Service) IDsOfPlaybook(ctx context.Context, orgID, playbookID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.IDsOfPlaybook(ctx, orgID, playbookID)
}

// runState is a template with the parts of it a run uses that the row
// itself does not hold: its own vault passwords and its follow-ups.
type runState struct {
	t         Template
	vault     []VaultCredential
	followUps []FollowUp
}

// watches reports whether a write by actor is handed on when it changes
// what the template runs: only an assistant's is.
func (s *Service) watches(actor assistantchange.Actor) bool {
	return actor.Assistant && s.watcher != nil
}

// stateOf reads the template's vault passwords and follow-ups beside t, for
// a write that is watched.
func (s *Service) stateOf(ctx context.Context, t Template) (runState, error) {
	vault, err := s.store.ListVaultCredentials(ctx, t.OrgID, t.ID)
	if err != nil {
		return runState{}, fmt.Errorf("list template vault credentials: %w", err)
	}
	followUps, err := s.store.ListFollowUps(ctx, t.OrgID, t.ID)
	if err != nil {
		return runState{}, fmt.Errorf("list template follow-ups: %w", err)
	}
	return runState{t: t, vault: vault, followUps: followUps}, nil
}

// handOn tells the watcher about actor's write from before to after when
// it is an assistant's and moved a field the run uses. It runs inside the
// write's transaction, so the watcher's writes land with it or not at all.
func (s *Service) handOn(ctx context.Context, orgID uuid.UUID, actor assistantchange.Actor, before, after runState) error {
	if !s.watches(actor) {
		return nil
	}
	fields := s.runChanges(ctx, orgID, before, after)
	if len(fields) == 0 {
		return nil
	}
	return s.watcher.Changed(ctx, orgID, assistantchange.New(assistantchange.SubjectTemplate, after.t.ID, after.t.Name, fields, actor))
}

// runChanges lists the fields a run uses that moved from before to after,
// in a fixed order, each as a person reads it; a secret value is masked.
// An extra var compares by its stored value, so a secret replaced by
// another shows as a masked change.
func (s *Service) runChanges(ctx context.Context, orgID uuid.UUID, before, after runState) []assistantchange.Field {
	b, a := before.t, after.t
	var out []assistantchange.Field
	add := func(name string, old, next any) {
		out = append(out, assistantchange.Field{Name: name, Old: old, New: next})
	}
	if b.PlaybookID != a.PlaybookID {
		add("playbook", s.playbookLabel(ctx, orgID, b.PlaybookID), s.playbookLabel(ctx, orgID, a.PlaybookID))
	}
	if !sameScope(b.Scope, a.Scope) {
		add("scope", s.scopeLabel(ctx, orgID, b.Scope), s.scopeLabel(ctx, orgID, a.Scope))
	}
	if b.Limit != a.Limit {
		add("limit", optional(b.Limit), optional(a.Limit))
	}
	if !reflect.DeepEqual(b.ExtraVarsSchema, a.ExtraVarsSchema) {
		add("extra_vars_schema", schemaLabel(b.ExtraVarsSchema), schemaLabel(a.ExtraVarsSchema))
	}
	oldVars, newVars := maskExtraVars(b.ExtraVarsSchema, b.ExtraVars), maskExtraVars(a.ExtraVarsSchema, a.ExtraVars)
	for _, name := range varNames(b.ExtraVars, a.ExtraVars) {
		if !reflect.DeepEqual(b.ExtraVars[name], a.ExtraVars[name]) {
			add("extra_vars."+name, oldVars[name], newVars[name])
		}
	}
	if !sameID(b.CredentialID, a.CredentialID) {
		add("credential", s.credentialLabel(ctx, orgID, b.CredentialID), s.credentialLabel(ctx, orgID, a.CredentialID))
	}
	if oldVault, newVault := s.vaultLabels(ctx, orgID, before.vault), s.vaultLabels(ctx, orgID, after.vault); !slices.Equal(oldVault, newVault) {
		add("vault_identities", oldVault, newVault)
	}
	if b.VarsMode != a.VarsMode {
		add("vars_mode", string(b.VarsMode), string(a.VarsMode))
	}
	if b.AgentEnrollment != a.AgentEnrollment {
		add("agent_enrollment", b.AgentEnrollment, a.AgentEnrollment)
	}
	if oldNames, newNames := followUpNames(before.followUps), followUpNames(after.followUps); !slices.Equal(oldNames, newNames) {
		add("follow_ups", oldNames, newNames)
	}
	return out
}

func optional(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameScope(a, b Scope) bool {
	return sameID(a.GroupID, b.GroupID) && slices.Equal(a.HostIDs, b.HostIDs)
}

// varNames is every top-level name of a and b, sorted.
func varNames(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// playbookLabel names a playbook: a repository playbook by its path, an
// editor playbook by its id.
func (s *Service) playbookLabel(ctx context.Context, orgID, id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	if p, err := s.playbooks.Get(ctx, orgID, id); err == nil && p.Path != nil {
		return *p.Path
	}
	return "editor playbook " + id.String()
}

// scopeLabel says which hosts a scope selects: the group by name, the
// hosts by name (an unknown one by id).
func (s *Service) scopeLabel(ctx context.Context, orgID uuid.UUID, sc Scope) any {
	if sc.Empty() {
		return "all managed hosts"
	}
	var parts []string
	if sc.GroupID != nil {
		name := sc.GroupID.String()
		if g, err := s.groups.GetGroup(ctx, orgID, *sc.GroupID); err == nil && g.Name != "" {
			name = g.Name
		}
		parts = append(parts, "group "+name)
	}
	if len(sc.HostIDs) > 0 {
		names := map[uuid.UUID]string{}
		if list, err := s.hosts.ListAllHosts(ctx, orgID); err == nil {
			for _, h := range list {
				names[h.ID] = h.Name
			}
		}
		hosts := make([]string, len(sc.HostIDs))
		for i, id := range sc.HostIDs {
			hosts[i] = id.String()
			if n := names[id]; n != "" {
				hosts[i] = n
			}
		}
		parts = append(parts, "hosts "+strings.Join(hosts, ", "))
	}
	return strings.Join(parts, "; ")
}

// credentialLabel names a credential, or none.
func (s *Service) credentialLabel(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	if c, err := s.creds.Get(ctx, orgID, *id); err == nil {
		return c.Name
	}
	return id.String()
}

// vaultLabels names a vault password set as "label: credential", in label
// order; never nil.
func (s *Service) vaultLabels(ctx context.Context, orgID uuid.UUID, items []VaultCredential) []string {
	out := make([]string, len(items))
	for i, v := range items {
		out[i] = fmt.Sprintf("%s: %v", v.Label, s.credentialLabel(ctx, orgID, &v.CredentialID))
	}
	sort.Strings(out)
	return out
}

func followUpNames(list []FollowUp) []string {
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.Name
	}
	return out
}

// schemaLabel is a schema's fields as plain JSON values, a secret field's
// default left out; nil for no schema.
func schemaLabel(sc *schema.Schema) any {
	if sc == nil {
		return nil
	}
	out := make([]any, len(sc.Fields))
	for i, f := range sc.Fields {
		if f.Type == schema.TypeSecret {
			f.Default = nil
		}
		raw, err := json.Marshal(f)
		if err != nil {
			out[i] = f.Name
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			out[i] = f.Name
			continue
		}
		out[i] = v
	}
	return out
}
