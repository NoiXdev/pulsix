package varsource

import (
	"errors"
	"path"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// roleSpec is a role's spec file within a walk. What it keeps between uses
// is what holds for every entry point of the file: whether it is missing,
// failed or declares no specs, and the file's budget and notes (y). The
// parsed tree is let go after each use, so a later use of another entry
// point reads and parses the file again (backlog 7.113): a walk keeps no
// tree of a role it is done with, and nothing of an entry point no use
// asks for.
type roleSpec struct {
	name    string // path in the tree from
	from    roleSource
	missing bool // no such file
	failed  bool // not read: too large, not valid YAML, … (noted once)
	noSpecs bool // a meta/main file without an argument_specs key
	y       *yamlFile
	parsed  bool       // root is the tree of the current use
	root    *yaml.Node // nil for an empty document
	uses    map[string]*specUse
}

// specUse is what a use of an entry point leaves for a later use of the
// same entry point: its state, its fields' names and sources, and the
// notes it raised. A later use adds the same notes and, as every name is
// declared by then, the same duplicate_name hints, without reading the
// file again. roleSeen already skips a role and entry point used before,
// so only two entry point names that are alike as shown (clipped to
// maxRefBytes) meet here; only such a name keeps one.
type specUse struct {
	state  RoleState
	fields []schema.Field
	log    noteLog
}

// noteLog is the notes an entry point's mapping raised, but for the
// duplicate keys, which a file notes once (dupsNoted): the notes
// a later use of the entry point raises again. It keeps up to
// maxNotesPerFile, as many as the file could keep, and counts the rest.
type noteLog struct {
	notes []Note
	more  int
}

func (l *noteLog) add(n Note) {
	switch {
	case n.Code == codeDuplicateKey:
	case len(l.notes) < maxNotesPerFile:
		l.notes = append(l.notes, n)
	default:
		l.more++
	}
}

// spec returns the walk's record of the spec file name (a path in the
// tree from), reading and parsing the file on its first use. Its notes join
// the playbook's where it was first read; they and its fields name the
// file as shown (with the tree's prefix).
func (w *walk) spec(name string, from roleSource) *roleSpec {
	key := specKey{file: name, from: from}
	if s, ok := w.specs[key]; ok {
		return s
	}
	s := &roleSpec{name: name, from: from}
	w.specs[key] = s
	w.parseSpec(s)
	return s
}

// parseSpec reads and parses s's file for the use at hand. A read after
// the first, for another entry point, is charged as the first was: its
// bytes to the walk's (read), and what the use maps to the file's and the
// walk's steps and bytes (account), against the same caps.
func (w *walk) parseSpec(s *roleSpec) {
	fsys, prefix := w.tree(s.from)
	file := prefix + s.name
	data, err := w.read(fsys, s.name)
	switch {
	case isNotExist(err) && s.y == nil:
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
			return
		}
		if s.y == nil {
			s.y = newYAMLFile(file)
			s.y.dups = &dupsNoted{} // parsed again for each entry point
			w.sources = append(w.sources, noteSource{y: s.y})
		}
		s.y.dups.parsed(&doc)
		s.root, s.parsed = documentRoot(&doc), true
	}
}

// release lets the tree of the current use go.
func (s *roleSpec) release() {
	s.root, s.parsed = nil, false
	s.y.release()
}

// readRole reads the argument specs of a found role for r's entry point:
// read, no_specs, or unreadable (a note says why).
func (w *walk) readRole(t roleTarget, r roleRef) RoleState {
	for _, name := range specNames {
		s := w.spec(path.Join(t.dir, "meta", name), t.from)
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

// readRoleSpec maps r's entry point of a spec file. A meta/main file
// without an argument_specs key declares no specs. A file, an
// argument_specs value or an entry point of the wrong shape, or a file
// that runs out of budget, is unreadable.
func (w *walk) readRoleSpec(s *roleSpec, r roleRef, metaMain bool) RoleState {
	y, ep := s.y, r.shownEntryPoint()
	switch u := s.uses[ep]; {
	case y.exceeded():
		return RoleUnreadable
	case s.noSpecs:
		return RoleNoSpecs
	case u != nil:
		y.note(u.log.notes...)
		y.dropped += u.log.more
		w.addSpecResult(specResult{fields: u.fields})
		return u.state
	}
	if !s.parsed {
		w.parseSpec(s)
		if !s.parsed {
			return RoleUnreadable
		}
	}
	defer s.release()
	if metaMain && (isNull(s.root) || s.root.Kind == yaml.MappingNode) && isNull(y.lookup(s.root, "argument_specs")) {
		w.account(y)
		if y.exceeded() {
			return RoleUnreadable
		}
		s.noSpecs = true
		return RoleNoSpecs
	}
	var u *specUse
	if strings.HasSuffix(ep, ellipsis) {
		u = &specUse{}
		y.log = &u.log
	}
	res := mapEntryPoint(y, s.root, r.shownName(), ep, schema.SourceArgumentSpec)
	y.log = nil
	w.account(y)
	if y.exceeded() {
		return RoleUnreadable
	}
	w.addSpecResult(res)
	state := RoleRead
	if res.broken {
		state = RoleUnreadable
	}
	if u != nil {
		u.state = state
		for _, f := range res.fields {
			u.fields = append(u.fields, schema.Field{Name: f.Name, Source: f.Source})
		}
		if s.uses == nil {
			s.uses = map[string]*specUse{}
		}
		s.uses[ep] = u
	}
	return state
}
