package varsource

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// InstalledRolesPrefix starts the path of a file read from the roles
// installed from requirements.yml, as notes and field sources show it:
// "(requirements)/<role>/meta/argument_specs.yml".
const InstalledRolesPrefix = "(requirements)/"

// roleTarget is where a role name leads from a playbook's directory: a
// state, and the role's directory when found.
type roleTarget struct {
	state     RoleState // RoleRead stands for "found"; reading decides the rest
	dir       string
	installed bool   // dir is in the installed roles, not the repository
	file      bool   // dir is not a directory: Ansible takes it for a role without content
	where     string // where a role not found was looked for
}

// roleKey identifies a role use within a walk: the role's directory when
// found (two names may lead to one role), else its state and name.
// installed keeps a directory of the installed roles apart from a
// repository directory of the same name.
type roleKey struct {
	id, entryPoint string
	installed      bool
}

// specKey identifies a spec file within a walk: its path in the
// repository, or in the installed roles.
type specKey struct {
	file      string
	installed bool
}

// specNames are the files Ansible reads a role's argument specs from, in
// its order: meta/argument_specs.*, else the argument_specs key of
// meta/main.*.
var specNames = []string{
	"argument_specs.yml", "argument_specs.yaml", "argument_specs.json",
	"main.yml", "main.yaml", "main.json",
}

// useRoles resolves and reads a playbook file's role uses in order. A role
// and entry point the walk used before adds nothing.
func (w *walk) useRoles(f *playFile) {
	for _, r := range f.refs {
		if w.over != "" {
			return
		}
		w.useRole(f, r)
	}
}

func (w *walk) useRole(f *playFile, r roleRef) {
	var t roleTarget
	switch {
	case r.invalid:
		t = roleTarget{state: RoleUnreadable} // noted where it was read
	case r.dynamic:
		t = roleTarget{state: RoleDynamic}
	default:
		t = w.target(r.name, f.dir)
	}
	key := roleKey{id: t.dir, entryPoint: r.entryPoint, installed: t.installed}
	if t.dir == "" {
		key.id = string(t.state) + "\x00" + r.name
	}
	if w.roleSeen[key] {
		return
	}
	if len(w.roleSeen) == maxWalkRoles {
		w.giveUp(fmt.Sprintf("it uses more than %d roles", maxWalkRoles))
		return
	}
	w.roleSeen[key] = true
	y, name := f.y, short(r.name)
	switch t.state {
	case RoleUnreadable: // an invalid use, noted where it was read
	case RoleDynamic:
		what := "the role name"
		if r.dynamic {
			what = "tasks_from"
		}
		y.note(hint(codeRoleDynamic, y.file, r.line, "role %q: %s is a template the run evaluates; its variables cannot be read", name, what))
	case RoleCollection:
		y.note(hint(codeRoleCollection, y.file, r.line, "role %q comes from a collection, which is not part of the repository; its variables are not read", name))
	case RoleInline:
		y.note(hint(codeInlineRolesUnresolved, y.file, r.line, "an inline playbook has no repository to find role %q in; its variables are not read", name))
	case RoleNotFound:
		y.note(warning(codeRoleNotFound, y.file, r.line, "role %q not found %s; its variables are not read", name, t.where))
	default:
		if t.file {
			t.state = RoleNoSpecs
			shown := clip(t.dir, maxRefBytes)
			if t.installed {
				shown = InstalledRolesPrefix + shown
			}
			y.note(hint(codeRoleWithoutSpecs, y.file, r.line, "role %q is the file %s, which Ansible takes for a role without content; it adds no fields", name, shown))
			break
		}
		t.state = w.readRole(t, r)
		if t.state == RoleNoSpecs {
			y.note(hint(codeRoleWithoutSpecs, y.file, r.line, "role %q declares no argument specs (meta/argument_specs.yml); it adds no fields", name))
		}
	}
	w.roles = append(w.roles, RoleUse{Name: r.shownName(), EntryPoint: r.shownEntryPoint(), State: t.state})
}

// shownName and shownEntryPoint are the role use as RoleUse and the
// fields' sources name it, the same in both so they can be matched.
func (r roleRef) shownName() string       { return clip(r.name, maxRefBytes) }
func (r roleRef) shownEntryPoint() string { return clip(r.entryPoint, maxRefBytes) }

// target finds a role by name from a playbook directory: a template is
// dynamic; an inline playbook has no repository; any other name, plain or
// with a slash, is looked up where ansible-playbook looks for it (see
// roleCandidates), and a name without a slash and with three or more
// dotted parts found nowhere is a collection role
// (namespace.collection.role). roles/ at the repository root is not
// searched for a playbook in a subdirectory: the run never finds a role
// there (backlog 7.90).
func (w *walk) target(name, dir string) roleTarget {
	switch {
	case templated(name):
		return roleTarget{state: RoleDynamic}
	case w.p.inline():
		return roleTarget{state: RoleInline}
	}
	memo := [2]string{dir, name}
	if t, ok := w.targets[memo]; ok {
		return t
	}
	t := w.findRole(name, dir)
	w.targets[memo] = t
	return t
}

// roleCandidate is a place a role name may lead to: a path in the
// repository, or in the installed roles.
type roleCandidate struct {
	path      string
	installed bool
}

// roleCandidates are the places ansible-playbook looks for the role name
// of a playbook in dir, in its order. ansible-core 2.21
// (RoleDefinition._load_role_path, backlog 7.91) tests, for every name,
// plain or with a slash, the entries of [<dir>/roles, roles_path, <dir>]
// joined with the name, and takes the first that exists. The parser mirrors
// that with <dir>/roles/<name>, the installed roles' <name> (when there are
// any), then <dir>/<name>. Names are joined lexically, as Ansible's
// realpath does for a path without a symlink ahead of a "..". A candidate
// that leaves its tree is left out: in the repository a path equal to ".."
// or starting with "../", in the installed roles a path that is not an
// fs.ValidPath.
//
// Left out on purpose:
//   - the role's dependents' directory (role_basedir): the parser reads no
//     role dependencies;
//   - the system entries of roles_path (/usr/share/ansible/roles and
//     /etc/ansible/roles): outside the repository;
//   - Ansible's last step, the name against the working directory: the run's
//     working directory holds no file of the clone.
func roleCandidates(name, dir string, withInstalled bool) []roleCandidate {
	var out []roleCandidate
	inRepo := func(p string) {
		if p != ".." && !strings.HasPrefix(p, "../") {
			out = append(out, roleCandidate{path: p})
		}
	}
	inRepo(path.Join(dir, "roles", name))
	if c := path.Clean(name); withInstalled && fs.ValidPath(c) {
		out = append(out, roleCandidate{path: c, installed: true})
	}
	inRepo(path.Join(dir, name))
	return out
}

func (w *walk) findRole(name, dir string) roleTarget {
	switch {
	case strings.HasPrefix(name, "/"):
		return roleTarget{state: RoleNotFound, where: "inside the repository"}
	case name == "" || name == "." || name == "..":
		return roleTarget{state: RoleNotFound, where: "(not a role name)"}
	}
	cands := roleCandidates(name, dir, w.p.roles != nil)
	var tried []string
	installed := false
	for _, c := range cands {
		fsys := w.p.fsys
		if c.installed {
			fsys, installed = w.p.roles, true
		} else {
			tried = append(tried, c.path)
		}
		// Ansible tests os.path.exists, false on any error (a missing
		// path, ENOTDIR below a file, no permission), so any error means
		// "not here".
		info, err := fs.Stat(fsys, c.path)
		if err != nil {
			continue
		}
		return roleTarget{state: RoleRead, dir: c.path, installed: c.installed, file: !info.IsDir()}
	}
	if !strings.Contains(name, "/") && strings.Count(name, ".") >= 2 {
		return roleTarget{state: RoleCollection}
	}
	if len(tried) == 0 {
		return roleTarget{state: RoleNotFound, where: "inside the repository"}
	}
	where := "at " + clip(strings.Join(tried, " or "), maxRefBytes)
	if installed {
		where += " or among the roles installed from requirements.yml"
	}
	return roleTarget{state: RoleNotFound, where: where}
}

// roleSpec is a role's spec file as read once per walk: many entry points
// of one file (tasks_from) share one parse and one budget.
type roleSpec struct {
	missing bool // no such file
	failed  bool // not read: too large, not valid YAML, … (noted once)
	y       *yamlFile
	root    *yaml.Node
}

// spec reads and parses the spec file name (a path in the repository, or
// in the installed roles) once per walk. Its notes join the playbook's
// where it was first read; they and its fields name the file as shown
// (InstalledRolesPrefix before a path in the installed roles).
func (w *walk) spec(name string, installed bool) *roleSpec {
	key := specKey{file: name, installed: installed}
	if s, ok := w.specs[key]; ok {
		return s
	}
	fsys, file := w.p.fsys, name
	if installed {
		fsys, file = w.p.roles, InstalledRolesPrefix+name
	}
	s := &roleSpec{}
	data, err := w.read(fsys, name)
	switch {
	case isNotExist(err):
		s.missing = true
	case errors.Is(err, errGivenUp):
		s.failed = true
	case err != nil:
		s.failed = true
		w.addNotes(readNote(file, err))
	default:
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			s.failed = true
			w.addNotes(invalidYAML(file, err))
			break
		}
		s.y, s.root = newYAMLFile(file), documentRoot(&doc)
		w.sources = append(w.sources, noteSource{y: s.y})
	}
	w.specs[key] = s
	return s
}

// readRole reads the argument specs of a found role for r's entry point:
// read, no_specs, or unreadable (a note says why).
func (w *walk) readRole(t roleTarget, r roleRef) RoleState {
	for _, name := range specNames {
		s := w.spec(path.Join(t.dir, "meta", name), t.installed)
		switch {
		case s.missing:
			continue
		case s.failed:
			return RoleUnreadable
		}
		return w.readRoleSpec(s, r, strings.HasPrefix(name, "main."))
	}
	return RoleNoSpecs
}

// readRoleSpec maps r's entry point of a parsed spec file. A meta/main
// file without an argument_specs key declares no specs. A file, an
// argument_specs value or an entry point of the wrong shape, or a file
// that runs out of budget, is unreadable.
func (w *walk) readRoleSpec(s *roleSpec, r roleRef, metaMain bool) RoleState {
	y := s.y
	if y.exceeded() {
		return RoleUnreadable
	}
	if metaMain && (isNull(s.root) || s.root.Kind == yaml.MappingNode) && isNull(y.lookup(s.root, "argument_specs")) {
		w.account(y)
		if y.exceeded() {
			return RoleUnreadable
		}
		return RoleNoSpecs
	}
	res := mapEntryPoint(y, s.root, r.shownName(), r.shownEntryPoint(), schema.SourceArgumentSpec)
	w.account(y)
	if y.exceeded() {
		return RoleUnreadable
	}
	w.addSpecResult(res)
	if res.broken {
		return RoleUnreadable
	}
	return RoleRead
}
