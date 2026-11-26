package varsource

import (
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// The vault read's part in the playbook walk (vault.go): the variables a
// playbook file sets to vault-encrypted values and the vars_files it
// names.

// playVault records the vault-encrypted variables a playbook file sets:
// key is the variable's name, value what it is set to.
func (f *playFile) playVault(key, value *yaml.Node) {
	if f.vaultSearch == nil {
		f.vaultSearch = newVaultSearch()
	}
	if f.vaultSearch.holds(value, 0) {
		f.vault = append(f.vault, key.Value)
	}
}

// varsFiles records a play's static vars_files. An entry the read cannot
// follow (templated, a list (Ansible's first-found form), not text,
// absolute) leaves the names not all known.
func (f *playFile) varsFiles(n *yaml.Node) {
	r := resolve(n)
	if isNull(r) {
		return
	}
	if r.Kind == yaml.ScalarNode {
		f.addVarsFile(r)
		return
	}
	if r.Kind != yaml.SequenceNode {
		f.varsFilesCut = true
		return
	}
	for _, it := range f.y.items(r) {
		if it = resolve(it); it != nil && it.Kind == yaml.ScalarNode {
			f.addVarsFile(it)
		} else if !isNull(it) {
			f.varsFilesCut = true
		}
	}
}

func (f *playFile) addVarsFile(n *yaml.Node) {
	text, ok := f.y.scalarText(n)
	switch {
	case ok && text == "":
	case !ok || templated(text) || path.IsAbs(text) || len(f.varsFileRefs) == maxWalkFiles:
		f.varsFilesCut = true
	default:
		f.varsFileRefs = append(f.varsFileRefs, text)
	}
}

// useVault hands what a read playbook file set to the repository's vault
// read, when the walk has one (a repo read, not a single playbook). A
// vars_files entry outside the read root is not read: the names are then
// not all known.
func (w *walk) useVault(file string, f *playFile) {
	v := w.p.vault
	if v == nil {
		return
	}
	for _, name := range f.vault {
		v.add(name, file)
	}
	if f.varsFilesCut || f.y.mergeCut || (f.vaultSearch != nil && f.vaultSearch.deep) {
		v.incomplete = true
	}
	for _, ref := range f.varsFileRefs {
		p := path.Clean(path.Join(f.dir, ref))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || !fs.ValidPath(p) {
			v.incomplete = true
			continue
		}
		v.readVarsFile(w.p.fsys, p, p)
	}
}

// vaultIncomplete records that a playbook file was not read: the
// variables it sets are not known.
func (w *walk) vaultIncomplete() {
	if w.p.vault != nil {
		w.p.vault.incomplete = true
	}
}
