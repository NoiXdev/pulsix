package repos

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/assistantchange"
)

// SetChangeWatcher wires the watcher an assistant's run-relevant change is
// handed to (spec 2026-11-25 C2): it pauses what runs the repository's
// playbooks, or syncs it, until a person reviews it. Without one nothing
// is handed on.
func (s *Service) SetChangeWatcher(w assistantchange.Watcher) { s.watcher = w }

// handOn tells the watcher about actor's update of a repository from
// before to after (each with its vault identities) when it is an
// assistant's and moved what a sync fetches or how a run reads the files.
// It runs inside the update's transaction.
func (s *Service) handOn(ctx context.Context, orgID uuid.UUID, actor assistantchange.Actor, before, after Repo) error {
	if !actor.Assistant || s.watcher == nil {
		return nil
	}
	fields := s.runChanges(ctx, orgID, before, after)
	if len(fields) == 0 {
		return nil
	}
	return s.watcher.Changed(ctx, orgID, assistantchange.New(assistantchange.SubjectRepo, after.ID, after.Name, fields, actor))
}

// runChanges lists the fields a sync or a run uses that moved, in a fixed
// order; the URL without its secret.
func (s *Service) runChanges(ctx context.Context, orgID uuid.UUID, b, a Repo) []assistantchange.Field {
	var out []assistantchange.Field
	add := func(name string, old, next any) {
		out = append(out, assistantchange.Field{Name: name, Old: old, New: next})
	}
	if b.URL != a.URL {
		add("url", RedactURL(b.URL), RedactURL(a.URL))
	}
	if b.Branch != a.Branch {
		add("branch", b.Branch, a.Branch)
	}
	if b.Subpath != a.Subpath {
		add("subpath", optional(b.Subpath), optional(a.Subpath))
	}
	if !sameID(b.CredentialID, a.CredentialID) {
		add("credential", s.credentialLabel(ctx, orgID, b.CredentialID), s.credentialLabel(ctx, orgID, a.CredentialID))
	}
	if oldVault, newVault := s.vaultLabels(ctx, orgID, b.VaultIdentities), s.vaultLabels(ctx, orgID, a.VaultIdentities); !slices.Equal(oldVault, newVault) {
		add("vault_identities", oldVault, newVault)
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

// credentialLabel names a credential, or none.
func (s *Service) credentialLabel(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	if c, err := s.creds.Get(ctx, orgID, *id); err == nil && c.Name != "" {
		return c.Name
	}
	return id.String()
}

// vaultLabels names a vault password set as "label: credential", sorted;
// never nil.
func (s *Service) vaultLabels(ctx context.Context, orgID uuid.UUID, items []VaultIdentity) []string {
	out := make([]string, len(items))
	for i, v := range items {
		out[i] = fmt.Sprintf("%s: %v", v.Label, s.credentialLabel(ctx, orgID, &v.CredentialID))
	}
	sort.Strings(out)
	return out
}
