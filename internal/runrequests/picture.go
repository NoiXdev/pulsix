package runrequests

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
)

// Picture is what a run request would use if it were approved now (spec
// 2026-11-16 assistant access, "Run requests"): the approval page shows it,
// and the approval's fingerprint covers it. It never carries a secret:
// credentials appear by name, extra vars with their secrets masked, and the
// group and membership vars only as VarsDigest.
type Picture struct {
	TemplateID       string          `json:"template_id"`
	TemplateName     string          `json:"template_name"`
	TemplateRevision int64           `json:"template_revision"`
	Limit            string          `json:"limit"`
	OnlyHost         string          `json:"only_host,omitempty"`
	Playbook         PicturePlaybook `json:"playbook"`
	Repo             *PictureRepo    `json:"repo,omitempty"`
	Targets          []PictureHost   `json:"targets"`
	SSHCredential    string          `json:"ssh_credential,omitempty"`
	VaultIdentities  []PictureVault  `json:"vault_identities"`
	ExtraVars        map[string]any  `json:"extra_vars"`
	Retry            *PictureRetry   `json:"retry,omitempty"`
	VarsDigest       string          `json:"vars_digest"`
}

// PicturePlaybook names the playbook a run uses.
type PicturePlaybook struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	// InlineDigest is the SHA-256 of an editor playbook's YAML, so an edit
	// changes the fingerprint without the page having to show the YAML.
	InlineDigest string `json:"inline_digest,omitempty"`
}

// PictureRepo is the repository a repository playbook comes from.
type PictureRepo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	Subpath string `json:"subpath"`
}

// PictureHost is one target host as the run would reach it.
type PictureHost struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Address    string `json:"address"`
	SSHUser    string `json:"ssh_user,omitempty"`
	SSHPort    int    `json:"ssh_port,omitempty"`
	JumpHost   string `json:"jump_host,omitempty"`
	Credential string `json:"credential,omitempty"`
}

// PictureVault is one vault identity, by label and credential name.
type PictureVault struct {
	Label      string `json:"label"`
	Credential string `json:"credential"`
}

// PictureRetry is what a retry request would run again.
type PictureRetry struct {
	JobID       string   `json:"job_id"`
	FailedHosts []string `json:"failed_hosts"`
}

// Fingerprint is the SHA-256 (hex) over the request id and the canonical
// JSON of p. encoding/json writes struct fields in declaration order and
// map keys sorted, so the only order left to fix is the slices': targets by
// id, vault identities by label, failed hosts as given sorted.
func Fingerprint(requestID uuid.UUID, p Picture) string {
	p.Targets = slices.Clone(p.Targets)
	slices.SortFunc(p.Targets, func(a, b PictureHost) int { return cmp.Compare(a.ID, b.ID) })
	p.VaultIdentities = slices.Clone(p.VaultIdentities)
	slices.SortFunc(p.VaultIdentities, func(a, b PictureVault) int {
		return cmp.Or(cmp.Compare(a.Label, b.Label), cmp.Compare(a.Credential, b.Credential))
	})
	if p.Retry != nil {
		r := *p.Retry
		r.FailedHosts = slices.Clone(r.FailedHosts)
		slices.Sort(r.FailedHosts)
		p.Retry = &r
	}
	doc, err := json.Marshal(struct {
		Request uuid.UUID `json:"request"`
		Picture Picture   `json:"picture"`
	}{requestID, p})
	if err != nil {
		// Picture holds only strings, numbers, slices of those and a
		// decoded JSON document; Marshal cannot fail on it.
		panic("fingerprint: " + err.Error())
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}
