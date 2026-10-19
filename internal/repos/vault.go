// Resolution for v0.2e (spec docs/superpowers/specs/2026-10-19-v0.2e-
// multiple-vault-passwords-design.md): a run's labelled vault passwords are
// the union of its repository's set and its job template's, with the
// template winning a label collision (decision 2 — "the more specific beats
// the more general", the same precedence internal/rules.Resolve already
// gives org → group → host → item). ResolveVaultIdentities is pure: no IO,
// no clock, the same shape rules.Resolve and alerts.Mute have, so the
// correctness of the merge never depends on anything but its two arguments.
package repos

import (
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/vaultlabel"
)

// VaultIdentity is one labelled vault password, resolved for a run.
type VaultIdentity struct {
	CredentialID uuid.UUID
	Label        string
	FromTemplate bool // for the run detail: where this one came from
}

// ResolveVaultIdentities merges a repository's identities with its job
// template's. A label present on both resolves to the template's, which is
// the same org → group → host → item precedence internal/rules uses: the
// more specific beats the more general. The result is ordered by label so a
// run's ansible.cfg is byte-stable across ticks — without the sort, the
// merge is a map iteration, which Go deliberately randomises, and the same
// job would write a different file on every run for no reason.
//
// repo identities are copied in with FromTemplate forced false (the store
// never sets it, but a caller building one by hand should not be able to
// lie about its origin); template identities are copied in with it forced
// true. A label on both sides therefore always resolves with FromTemplate
// true, because the template's copy is written into the map second and
// unconditionally overwrites the repository's — decision 2's "the
// template's wins" needs no tie-break beyond insertion order, since there
// are only ever two sources and the second one named always wins.
func ResolveVaultIdentities(repo, template []VaultIdentity) []VaultIdentity {
	byLabel := make(map[string]VaultIdentity, len(repo)+len(template))
	for _, id := range repo {
		id.FromTemplate = false
		byLabel[id.Label] = id
	}
	for _, id := range template {
		id.FromTemplate = true
		byLabel[id.Label] = id
	}

	out := make([]VaultIdentity, 0, len(byLabel))
	for _, id := range byLabel {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// MaxVaultLabelLength bounds a vault identity label on the write path. The
// label column itself is unbounded text (migration 00059) and
// internal/worker/runner does not bound length either — this exists only so
// the generated OpenAPI document names a limit a client can rely on
// (backlog 4.40's lesson: a server-enforced bound left undescribed in the
// schema is itself a bug). A length bound is an API-contract concern, not a
// safety one — nothing about ansible.cfg cares how long a label is — so it
// stays here rather than moving into internal/vaultlabel's shared rule; the
// runner's own gate has no equivalent and does not need one.
const MaxVaultLabelLength = 100

// InvalidVaultLabel reports why label cannot be written into ansible.cfg's
// vault_identity_list, or "" when it is fine. The character rule is
// internal/vaultlabel's, shared with internal/worker/runner's own gate, so
// the write-time check here and the runner's last-chance check before it
// renders the config can never disagree about what ansible.cfg can safely
// contain — a disagreement would only surface as a run failing after a form
// already accepted the value. The length bound is checked here only (see
// MaxVaultLabelLength) and in runes, not bytes: huma validates its sibling
// maxLength:"100" tag with utf8.RuneCountInString (confirmed in
// huma/v2@v2.39.1/validate.go), and a byte count would reject a sub-100-rune
// label — any multi-byte character, such as a German umlaut, makes one —
// that huma's own schema check had already accepted.
func InvalidVaultLabel(label string) string {
	if reason := vaultlabel.Invalid(label); reason != "" {
		return reason
	}
	if utf8.RuneCountInString(label) > MaxVaultLabelLength {
		return fmt.Sprintf("label exceeds %d characters", MaxVaultLabelLength)
	}
	return ""
}
