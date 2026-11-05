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

// roleTarget is where a role name leads from a playbook's directory: a
// state, and the role's directory when found.
type roleTarget struct {
	state RoleState // RoleRead stands for "found"; reading decides the rest
	dir   string
	where string // where a role not found was looked for
}

// roleKey identifies a role use within a walk: the role's directory when
// found (two names may lead to one role), else its state and name.
type roleKey struct{ id, entryPoint string }

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
	key := roleKey{id: t.dir, entryPoint: r.entryPoint}
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
		t.state = w.readRole(t.dir, r)
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
// dynamic; an inline playbook has no repository; a name with a slash is a
// path relative to the playbook's directory; any other is looked up in
// <dir>/roles, then in roles/ at the root, and a name of three or more
// dotted parts found in neither is a collection role (namespace.collection.role),
// as Ansible resolves it.
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

func (w *walk) findRole(name, dir string) roleTarget {
	var candidates []string
	switch {
	case strings.Contains(name, "/"):
		p := path.Join(dir, name)
		if strings.HasPrefix(name, "/") || p == ".." || strings.HasPrefix(p, "../") {
			return roleTarget{state: RoleNotFound, where: "inside the repository"}
		}
		candidates = []string{p}
	case name == "" || name == "." || name == "..":
		return roleTarget{state: RoleNotFound, where: "(not a role name)"}
	default:
		candidates = []string{path.Join(dir, "roles", name)}
		if dir != "." {
			candidates = append(candidates, path.Join("roles", name))
		}
	}
	for _, c := range candidates {
		if info, err := fs.Stat(w.p.fsys, c); err == nil && info.IsDir() {
			return roleTarget{state: RoleRead, dir: c}
		}
	}
	if !strings.Contains(name, "/") && strings.Count(name, ".") >= 2 {
		return roleTarget{state: RoleCollection}
	}
	return roleTarget{state: RoleNotFound, where: "at " + clip(strings.Join(candidates, " or "), maxRefBytes)}
}

// roleSpec is a role's spec file as read once per walk: many entry points
// of one file (tasks_from) share one parse and one budget.
type roleSpec struct {
	missing bool // no such file
	failed  bool // not read: too large, not valid YAML, … (noted once)
	y       *yamlFile
	root    *yaml.Node
}

// spec reads and parses a role spec file once per walk. Its notes join the
// playbook's where it was first read.
func (w *walk) spec(file string) *roleSpec {
	if s, ok := w.specs[file]; ok {
		return s
	}
	s := &roleSpec{}
	data, err := w.read(file)
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
	w.specs[file] = s
	return s
}

// readRole reads the argument specs of a found role for r's entry point:
// read, no_specs, or unreadable (a note says why).
func (w *walk) readRole(dir string, r roleRef) RoleState {
	for _, name := range specNames {
		s := w.spec(path.Join(dir, "meta", name))
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
