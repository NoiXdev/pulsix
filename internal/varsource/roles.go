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
	t := roleTarget{state: RoleDynamic}
	if !r.dynamic {
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
// path relative to the playbook's directory; a dotted name is a collection
// role; any other is looked up in <dir>/roles, then in roles/ at the root.
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
	case strings.Contains(name, "."):
		return roleTarget{state: RoleCollection}
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
	return roleTarget{state: RoleNotFound, where: "at " + clip(strings.Join(candidates, " or "), maxRefBytes)}
}

// readRole reads the argument specs of a found role for r's entry point:
// read, no_specs, or unreadable (a note says why).
func (w *walk) readRole(dir string, r roleRef) RoleState {
	for _, name := range specNames {
		file := path.Join(dir, "meta", name)
		data, err := w.read(file)
		switch {
		case isNotExist(err):
			continue
		case errors.Is(err, errGivenUp):
			return RoleUnreadable
		case err != nil:
			w.addNotes(readNote(file, err))
			return RoleUnreadable
		}
		y := newYAMLFile(file)
		fields, notes, state := readRoleSpec(y, data, r.shownName(), r.shownEntryPoint(), strings.HasPrefix(name, "main."))
		w.account(y)
		w.addNotes(notes...)
		w.merge(fields)
		return state
	}
	return RoleNoSpecs
}

// readRoleSpec reads one role spec file. A meta/main file without an
// argument_specs key declares no specs.
func readRoleSpec(y *yamlFile, data []byte, role, entryPoint string, metaMain bool) ([]schema.Field, []Note, RoleState) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, []Note{invalidYAML(y.file, err)}, RoleUnreadable
	}
	root := documentRoot(&doc)
	if metaMain && isNull(y.lookup(root, "argument_specs")) {
		if n, over := y.tooComplex(); over {
			return nil, []Note{n}, RoleUnreadable
		}
		return nil, nil, RoleNoSpecs
	}
	fields, notes, ok, _ := readArgumentSpecRoot(y, root, role, entryPoint, schema.SourceArgumentSpec)
	if !ok {
		return nil, notes, RoleUnreadable
	}
	return fields, notes, RoleRead
}
