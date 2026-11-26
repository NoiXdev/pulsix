package varsource

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

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

// vaultTree is one walk: the file system, the prefix its files are named
// with, which files are variable files (match) and which directories are
// not walked (skip, when set).
type vaultTree struct {
	fsys        fs.FS
	prefix      string
	match, skip func(string) bool
}

// walk reads the variable files of fsys. Hidden directories are skipped,
// as discovery skips them, and so is a directory skip (when set) reports.
func (v *vaultSet) walk(fsys fs.FS, prefix string, match, skip func(string) bool) {
	t := vaultTree{fsys: fsys, prefix: prefix, match: match, skip: skip}
	root, err := fs.Stat(fsys, ".")
	if err != nil {
		v.listFile(&v.unread, prefix+".")
		return
	}
	v.walkDir(t, ".", []fs.FileInfo{root}, 0)
}

// walkDir reads the directory dir: ancestors are the directories it lies
// in, dir itself last, and links the directory links followed to reach
// it. The time limit is checked at every entry.
func (v *vaultSet) walkDir(t vaultTree, dir string, ancestors []fs.FileInfo, links int) {
	entries, err := fs.ReadDir(t.fsys, dir)
	if err != nil {
		// A directory that cannot be listed may hold variable files.
		v.listFile(&v.unread, t.prefix+dir)
		return
	}
	for _, e := range entries {
		if v.stopped() {
			return
		}
		if v.entries++; v.entries > maxVaultEntries {
			v.stop = true
			return
		}
		name := path.Join(dir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			v.followLink(t, name, ancestors, links)
		case e.IsDir():
			if t.skipped(name) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				v.listFile(&v.unread, t.prefix+name)
				continue
			}
			v.walkDir(t, name, withInfo(ancestors, info), links)
		case e.Type().IsRegular() && t.match(name):
			v.readVarsFile(t.fsys, name, t.prefix+name)
		}
	}
}

// followLink reads what the link at name leads to, as Ansible does, under
// name. A link the file system does not follow (it leaves the clone, or
// leads nowhere) is unread when variables could be behind it. A directory
// already being walked (a loop) is not walked again: its files are read
// where it starts; where the file system cannot tell, at most
// maxVaultLinks links are followed along one path and the names are Cut
// past it.
func (v *vaultSet) followLink(t vaultTree, name string, ancestors []fs.FileInfo, links int) {
	mayHold := t.match(name) || t.match(name+"/x.yml")
	info, err := fs.Stat(t.fsys, name)
	switch {
	case err != nil:
		if mayHold {
			v.listFile(&v.unread, t.prefix+name)
		}
	case info.Mode().IsRegular():
		if t.match(name) {
			v.readVarsFile(t.fsys, name, t.prefix+name)
		}
	case info.IsDir():
		if t.skipped(name) {
			return
		}
		for _, a := range ancestors {
			if os.SameFile(a, info) {
				return
			}
		}
		if links == maxVaultLinks {
			v.incomplete = true
			return
		}
		v.walkDir(t, name, withInfo(ancestors, info), links+1)
	}
}

// skipped reports a directory the walk leaves out.
func (t vaultTree) skipped(name string) bool {
	return strings.HasPrefix(path.Base(name), ".") || (t.skip != nil && t.skip(name))
}

// withInfo is ancestors with info appended, never sharing its array.
func withInfo(ancestors []fs.FileInfo, info fs.FileInfo) []fs.FileInfo {
	return append(ancestors[:len(ancestors):len(ancestors)], info)
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
	if v.stopped() {
		return
	}
	if v.files == maxVaultFilesRead {
		v.stop = true
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
		v.stop = true
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
	search := newVaultSearch()
	for _, p := range y.pairs(documentRoot(&doc)) {
		if search.holds(p.value, 0) {
			v.add(p.key.Value, file)
		}
	}
	switch {
	case y.exceeded():
		v.listFile(&v.unread, file)
	case search.deep || y.mergeCut:
		v.incomplete = true
	}
}
