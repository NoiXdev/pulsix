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
// A name counts when its value holds a vault-encrypted scalar anywhere
// inside it (a dictionary with one encrypted key replaces the secret just
// as well): a !vault tag, or a string that starts with $ANSIBLE_VAULT;.
// A variable file encrypted as a whole hides its names: it is listed as
// such. The read is bounded; when a limit or the time limit leaves files
// unread, Cut says so and the names are not all known.
package varsource

import (
	"context"
	"errors"
	"io/fs"
	"path"
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
	// or the report left playbooks out; names may be missing.
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
	// maxVaultDepth bounds how deep a value is searched for ciphertext.
	maxVaultDepth = 64
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
	bytes     int
	cut       bool
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
		v.cut = true
		return
	}
	v.names[k] = true
}

func (v *vaultSet) listFile(list *[]string, file string) {
	if len(*list) == maxVaultFiles {
		v.cut = true
		return
	}
	*list = append(*list, clip(file, maxRefBytes))
}

// result is the read as a Vault; cut adds a cut of the caller's.
func (v *vaultSet) result(cut bool) *Vault {
	out := &Vault{Names: make([]VaultName, 0, len(v.names)), EncryptedFiles: v.encrypted, Unread: v.unread, Cut: v.cut || cut}
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

// scanVaultFiles reads every variable file of the repository and of what
// its requirements installed.
func (v *vaultSet) scanVaultFiles(fsys fs.FS, in Installed) {
	v.walk(fsys, "", isVarsFile, nil)
	if in.Roles != nil {
		// One directory per role: <role>/defaults/… and <role>/vars/….
		v.walk(in.Roles, InstalledRolesPrefix, func(p string) bool { return isVarsFile("roles/" + p) }, nil)
	}
	if in.Collections != nil {
		// Only the collections' roles: <namespace>/<collection>/roles/….
		v.walk(in.Collections, InstalledCollectionsPrefix, isVarsFile, func(dir string) bool {
			return strings.Count(dir, "/") == 2 && path.Base(dir) != "roles"
		})
	}
}

// walk reads the files of fsys that match, named prefix + their path.
// Hidden directories are skipped, as discovery skips them, and so is a
// directory skip (when set) reports.
func (v *vaultSet) walk(fsys fs.FS, prefix string, match, skip func(string) bool) {
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if v.cut {
			return fs.SkipAll
		}
		if err != nil {
			if name == "." {
				return err
			}
			if entry != nil && entry.IsDir() {
				// A directory that cannot be listed may hold variable files.
				v.listFile(&v.unread, prefix+name)
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if name != "." && (strings.HasPrefix(entry.Name(), ".") || (skip != nil && skip(name))) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() && entry.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		if match(name) {
			v.readVarsFile(fsys, name, prefix+name)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		v.listFile(&v.unread, prefix+".")
	}
}

// isVarsFile reports whether p, a path in the repository, is a variable
// file Ansible may load by its place: under group_vars or host_vars, a
// role's defaults or vars, or under any directory named vars (vars_files
// usually live there), with no extension or .yml, .yaml or .json.
func isVarsFile(p string) bool {
	switch path.Ext(p) {
	case "", ".yml", ".yaml", ".json":
	default:
		return false
	}
	dirs := strings.Split(path.Dir(p), "/")
	for i, d := range dirs {
		switch {
		case d == "group_vars" || d == "host_vars" || d == "vars":
			return true
		case d == "defaults" && i >= 2 && dirs[i-2] == "roles":
			return true
		}
	}
	return false
}

// readVarsFile reads one variable file of fsys, reported as file, once.
func (v *vaultSet) readVarsFile(fsys fs.FS, name, file string) {
	if v.read[file] {
		return
	}
	if v.ctx.Err() != nil || v.files == maxVaultFilesRead {
		v.cut = true
		return
	}
	v.read[file] = true
	v.files++
	data, err := readLimited(fsys, name)
	v.bytes += len(data)
	switch {
	case isNotExist(err) || errors.Is(err, errNotRegular):
		return
	case err != nil:
		v.listFile(&v.unread, file)
		return
	}
	if v.bytes > maxVaultBytes {
		v.cut = true
		return
	}
	if strings.HasPrefix(strings.TrimLeft(string(data), " \t\r\n\ufeff"), vaultHeader) {
		v.listFile(&v.encrypted, file)
		return
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		v.listFile(&v.unread, file)
		return
	}
	y := newYAMLFile(file)
	memo := map[*yaml.Node]bool{}
	for _, p := range y.pairs(documentRoot(&doc)) {
		if holdsVault(p.value, memo, 0) {
			v.add(p.key.Value, file)
		}
	}
	if y.exceeded() {
		v.listFile(&v.unread, file)
	}
}

// isVaultValue reports a scalar Ansible reads as vault-encrypted: tagged
// !vault, or a string that starts with the vault header.
func isVaultValue(n *yaml.Node) bool {
	if n.Kind != yaml.ScalarNode {
		return false
	}
	if n.ShortTag() == "!vault" {
		return true
	}
	return n.ShortTag() == "!!str" && strings.HasPrefix(strings.TrimLeft(n.Value, " \t\r\n"), vaultHeader)
}

// holdsVault reports whether the value n holds a vault-encrypted scalar
// anywhere inside it. memo keeps each node's answer, so a node reached
// through many aliases is searched once; a node deeper than maxVaultDepth
// is not searched.
func holdsVault(n *yaml.Node, memo map[*yaml.Node]bool, depth int) bool {
	n = resolve(n)
	if n == nil || depth > maxVaultDepth {
		return false
	}
	if held, ok := memo[n]; ok {
		return held
	}
	memo[n] = false // a node that holds itself (an anchor in its own value) ends here
	held := isVaultValue(n)
	for _, c := range n.Content {
		if held {
			break
		}
		held = holdsVault(c, memo, depth+1)
	}
	memo[n] = held
	return held
}

// playVault records the vault-encrypted variables a playbook file sets:
// key is the variable's name, value what it is set to.
func (f *playFile) playVault(key, value *yaml.Node) {
	if f.vaultMemo == nil {
		f.vaultMemo = map[*yaml.Node]bool{}
	}
	if holdsVault(value, f.vaultMemo, 0) {
		f.vault = append(f.vault, key.Value)
	}
}

// varsFiles records a play's static vars_files: a templated entry, or one
// that is a list (Ansible's first-found form), is not read.
func (f *playFile) varsFiles(n *yaml.Node) {
	r := resolve(n)
	if isNull(r) {
		return
	}
	if r.Kind == yaml.ScalarNode {
		f.addVarsFile(r)
		return
	}
	for _, it := range f.y.items(r) {
		if it = resolve(it); it != nil && it.Kind == yaml.ScalarNode {
			f.addVarsFile(it)
		}
	}
}

func (f *playFile) addVarsFile(n *yaml.Node) {
	text, ok := f.y.scalarText(n)
	if !ok || text == "" || templated(text) || path.IsAbs(text) || len(f.varsFileRefs) == maxWalkFiles {
		return
	}
	f.varsFileRefs = append(f.varsFileRefs, text)
}

// useVault hands what a read playbook file set to the repository's vault
// read, when the walk has one (a repo read, not a single playbook).
func (w *walk) useVault(file string, f *playFile) {
	v := w.p.vault
	if v == nil {
		return
	}
	for _, name := range f.vault {
		v.add(name, file)
	}
	for _, ref := range f.varsFileRefs {
		p := path.Clean(path.Join(f.dir, ref))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || !fs.ValidPath(p) {
			continue
		}
		v.readVarsFile(w.p.fsys, p, p)
	}
}
