// The names of the repository's vault-encrypted variables (backlog 2.134).
// A run request's extra vars must never replace a secret, and a value the
// repository holds encrypted with Ansible Vault is one; the manager cannot
// decrypt it and must not, so the read reports where such values are set,
// by variable name and file, never by value:
//   - the top-level keys of a variable file: one under a group_vars or
//     host_vars directory, a role's defaults or vars (in the repository,
//     the installed roles and the installed collections' roles), any file
//     under a directory named vars, and a play's static vars_files;
//   - a play's vars, a role entry's vars and parameters, and an import's
//     vars, in every playbook file the walk reads.
//
// Links are followed as Ansible follows them, a linked file or directory
// read under the path that links to it (vault_walk.go); the reader's file
// system keeps them inside the clone.
//
// A name counts when its value holds a vault-encrypted scalar anywhere
// inside it (a dictionary with one encrypted key replaces the secret just
// as well): a scalar tagged !vault or !vault-encrypted (the two tags
// Ansible's loader reads as vault-encrypted), or any scalar whatever its
// tag whose text starts with $ANSIBLE_VAULT;. A variable file encrypted
// as a whole hides its names: it is listed as such. The read is bounded;
// when a limit or the time limit leaves files unread, or a source cannot
// be followed (a templated vars_files, a link that leads nowhere, a value
// nested too deep), Cut says so and the names are not all known.
package varsource

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// VaultName is a variable the repository sets to a vault-encrypted value,
// with the file that sets it. It never carries the value.
type VaultName struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// Vault is what the read found encrypted with Ansible Vault. A report
// without one comes from a worker that does not look (older than backlog
// 2.134): the names are then unknown, not absent.
type Vault struct {
	// Names sorted by name, then file.
	Names []VaultName `json:"names"`
	// EncryptedFiles are variable files encrypted as a whole: the names
	// they set are not known.
	EncryptedFiles []string `json:"encrypted_files"`
	// Unread are variable files that could not be read (too large, not
	// YAML, not readable): the names they set are not known.
	Unread []string `json:"unread"`
	// Cut: a limit, the time limit or a cancellation left files unread,
	// a source could not be followed, or the report left playbooks out;
	// names may be missing.
	Cut bool `json:"cut"`
}

// Complete reports whether every variable file and playbook was read: no
// file encrypted as a whole, none unread, nothing cut.
func (v *Vault) Complete() bool {
	return v != nil && !v.Cut && len(v.EncryptedFiles) == 0 && len(v.Unread) == 0
}

// Limits of the vault read.
const (
	// maxVaultNames bounds the names reported; past it the read is Cut.
	maxVaultNames = 2000
	// MaxVaultNames is maxVaultNames for the manager's check of a report.
	MaxVaultNames = maxVaultNames
	// maxVaultFiles bounds the encrypted and unread files listed.
	maxVaultFiles = 200
	// MaxVaultFiles is maxVaultFiles for the manager's check of a report.
	MaxVaultFiles = maxVaultFiles
	// maxVaultFilesRead bounds the variable files read.
	maxVaultFilesRead = 5000
	// maxVaultBytes bounds the bytes of variable files read.
	maxVaultBytes = 64 << 20
	// maxVaultDepth bounds how deep a value is searched for ciphertext;
	// past it the read is Cut.
	maxVaultDepth = 64
	// maxVaultEntries bounds the directory entries walked.
	maxVaultEntries = 200_000
	// maxVaultLinks bounds the directory links followed along one path
	// where the file system cannot tell a loop (os.SameFile).
	maxVaultLinks = 16
)

// vaultHeader starts every Ansible Vault ciphertext.
const vaultHeader = "$ANSIBLE_VAULT;"

// varNamePattern is what Ansible accepts as a variable name (a Python
// identifier, ASCII only), at most 128 characters.
var varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidVaultName reports whether name may be a variable's: what a report
// names otherwise is no variable.
func ValidVaultName(name string) bool { return varNamePattern.MatchString(name) }

// vaultSet gathers what one repository read finds.
type vaultSet struct {
	ctx       context.Context
	names     map[VaultName]bool
	encrypted []string
	unread    []string
	read      map[string]bool // variable files read, by reported name
	files     int
	entries   int // directory entries walked
	bytes     int
	// stop: a limit or the time limit ended the read; incomplete: a
	// source was not followed or a list was capped, the read goes on.
	// Either leaves the names Cut.
	stop, incomplete bool
}

func newVaultSet(ctx context.Context) *vaultSet {
	return &vaultSet{ctx: ctx, names: map[VaultName]bool{}, read: map[string]bool{}}
}

func (v *vaultSet) add(name, file string) {
	if !ValidVaultName(name) {
		return
	}
	k := VaultName{Name: name, File: clip(file, maxRefBytes)}
	if v.names[k] {
		return
	}
	if len(v.names) == maxVaultNames {
		v.incomplete = true
		return
	}
	v.names[k] = true
}

func (v *vaultSet) listFile(list *[]string, file string) {
	if len(*list) == maxVaultFiles {
		v.incomplete = true
		return
	}
	*list = append(*list, clip(file, maxRefBytes))
}

// result is the read as a Vault; cut adds a cut of the caller's.
func (v *vaultSet) result(cut bool) *Vault {
	out := &Vault{Names: make([]VaultName, 0, len(v.names)), EncryptedFiles: v.encrypted, Unread: v.unread, Cut: v.stop || v.incomplete || cut}
	for k := range v.names {
		out.Names = append(out.Names, k)
	}
	slices.SortFunc(out.Names, func(a, b VaultName) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.File, b.File)
	})
	if out.EncryptedFiles == nil {
		out.EncryptedFiles = []string{}
	}
	if out.Unread == nil {
		out.Unread = []string{}
	}
	slices.Sort(out.EncryptedFiles)
	slices.Sort(out.Unread)
	return out
}

// stopped reports whether the read has ended: a limit was hit before, or
// the time limit has passed now.
func (v *vaultSet) stopped() bool {
	if !v.stop && v.ctx.Err() != nil {
		v.stop = true
	}
	return v.stop
}

// isVaultValue reports a scalar Ansible reads as vault-encrypted: tagged
// !vault or !vault-encrypted, or whatever its tag, text that starts with
// the vault header (a tag Ansible does not decrypt still hands the
// ciphertext on, and a custom loader may decrypt it).
func isVaultValue(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
		return false
	}
	if t := n.ShortTag(); t == "!vault" || t == "!vault-encrypted" {
		return true
	}
	return strings.HasPrefix(strings.TrimLeft(n.Value, " \t\r\n"), vaultHeader)
}

// vaultSearch looks for ciphertext inside values of one parsed file. memo
// keeps each node's answer, so a node reached through many aliases is
// searched once; deep: a node lay deeper than maxVaultDepth and was not
// searched.
type vaultSearch struct {
	memo map[*yaml.Node]bool
	deep bool
}

func newVaultSearch() *vaultSearch { return &vaultSearch{memo: map[*yaml.Node]bool{}} }

// holds reports whether the value n holds a vault-encrypted scalar
// anywhere inside it.
func (s *vaultSearch) holds(n *yaml.Node, depth int) bool {
	n = resolve(n)
	if n == nil {
		return false
	}
	if depth > maxVaultDepth {
		s.deep = true
		return false
	}
	if held, ok := s.memo[n]; ok {
		return held
	}
	s.memo[n] = false // a node that holds itself (an anchor in its own value) ends here
	held := isVaultValue(n)
	for _, c := range n.Content {
		if held {
			break
		}
		held = s.holds(c, depth+1)
	}
	s.memo[n] = held
	return held
}
