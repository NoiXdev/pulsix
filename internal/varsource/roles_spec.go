package varsource

import (
	"errors"
	"path"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// roleSpec is a role's spec file as read once per walk: many entry points
// of one file (tasks_from) share one parse and one budget.
//
// The walk keeps what the parse yielded, never the tree (backlog 7.113): a
// parsed file is about 85 times its bytes, and a walk may read many. So the
// file is mapped for every entry point it declares at parse time, each
// entry point's fields and notes kept apart, and a role use replays the one
// it asks for. The tree and the file's node caches are dropped before spec
// returns. The entry points share one option budget (maxSpecFields): mapping
// them all is bounded by the file, not by how many entry points it declares
// or how its aliases multiply them.
type roleSpec struct {
	missing bool // no such file
	failed  bool // not read: too large, not valid YAML, … (noted once)
	y       *yamlFile

	// over: the file ran out of budget while it was mapped. It counts as not
	// read, and its one file_too_complex note replaces its others.
	over bool

	// What reading the file raised before any entry point, replayed with the
	// first use of the file: duplicate keys of the top-level mappings.
	common      captured
	commonShown bool

	noSpecs   bool  // a meta/main file without an argument_specs key
	bad       *Note // the file or its argument_specs is no mapping: every entry point is broken
	specsLine int   // the argument_specs key's line, 0 without one
	entries   map[string]*specEntry
}

// specEntry is one entry point of a spec file, mapped when the file was
// parsed.
type specEntry struct {
	res  specResult // fields carry no role name: a use sets its own
	read captured
}

// spec reads and parses the spec file name (a path in the tree from) once
// per walk. Its notes join the playbook's where it was first read; they and
// its fields name the file as shown (with the tree's prefix).
func (w *walk) spec(name string, from roleSource) *roleSpec {
	key := specKey{file: name, from: from}
	if s, ok := w.specs[key]; ok {
		return s
	}
	fsys, prefix := w.tree(from)
	file := prefix + name
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
		s.y = newYAMLFile(file)
		s.extract(documentRoot(&doc), strings.HasPrefix(path.Base(name), "main."))
		s.y.release()
		w.account(s.y)
		w.sources = append(w.sources, noteSource{y: s.y})
	}
	w.specs[key] = s
	return s
}

// extract maps every entry point of the parsed file with root (see
// roleSpec). metaMain is a meta/main file, whose argument_specs key may be
// absent. The entry points share one option budget, each one costing a unit
// besides its options; past it, or past the file's visit budget, the file is
// over.
func (s *roleSpec) extract(root *yaml.Node, metaMain bool) {
	y := s.y
	y.trackDuplicates()
	s.entries = map[string]*specEntry{}
	var specs pair
	var hasSpecs bool
	var list []pair // the entry points
	s.common = y.capture(func() {
		if metaMain && (isNull(root) || root.Kind == yaml.MappingNode) && isNull(y.lookup(root, "argument_specs")) {
			s.noSpecs = true
			return
		}
		specs, hasSpecs, s.bad = locateSpecs(y, root)
		if s.bad == nil && hasSpecs {
			s.specsLine = specs.key.Line
			list = y.pairs(specs.value) // its duplicate keys are noted here, once
		}
	})
	if s.noSpecs || s.bad != nil || y.exceeded() {
		s.over = y.exceeded()
		return
	}
	m := &specMapper{y: y, budget: maxSpecFields}
	for _, ep := range list {
		m.budget--
		if m.budget < 0 {
			y.giveUpOptions()
			break
		}
		if !y.spend(1) {
			y.giveUp()
			break
		}
		e := &specEntry{}
		e.read = y.capture(func() {
			e.res = mapEntry(y, m, ep, "", ep.key.Value, schema.SourceArgumentSpec)
		})
		if y.exceeded() { // the visit or byte budget ran out
			break
		}
		if m.exhausted {
			y.giveUpOptions()
			break
		}
		s.entries[ep.key.Value] = e
	}
	if s.over = y.exceeded(); s.over {
		s.entries = nil
	}
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
		return w.readRoleSpec(s, r)
	}
	return RoleNoSpecs
}

// readRoleSpec maps r's entry point of a parsed spec file. A meta/main
// file without an argument_specs key declares no specs. A file, an
// argument_specs value or an entry point of the wrong shape, or a file
// that runs out of budget, is unreadable.
func (w *walk) readRoleSpec(s *roleSpec, r roleRef) RoleState {
	y := s.y
	if !s.commonShown {
		s.commonShown = true
		y.replay(s.common)
	}
	switch {
	case s.over:
		return RoleUnreadable
	case s.noSpecs:
		return RoleNoSpecs
	case s.bad != nil:
		y.note(*s.bad)
		return RoleUnreadable
	}
	name := r.shownEntryPoint()
	e, found := s.entries[name]
	if found {
		y.replay(e.read)
	} else {
		y.note(missingEntryPoint(y.file, s.specsLine, name))
		e = &specEntry{}
	}
	res := e.res
	res.fields = withRole(res.fields, r.shownName())
	w.addSpecResult(res)
	if res.broken {
		return RoleUnreadable
	}
	return RoleRead
}

// withRole copies fields with the role use that reads them named in their
// sources: two names may lead to one role directory, and each shows its own.
func withRole(fields []schema.Field, role string) []schema.Field {
	out := make([]schema.Field, len(fields))
	for i, f := range fields {
		src := *f.Source
		src.Role = role
		f.Source = &src
		out[i] = f
	}
	return out
}
