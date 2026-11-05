package varsource

import (
	"slices"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// unreadable lists the declared names that map onto no field from any
// source, sorted, up to maxListedSetNames. cut is true when any cap left
// declared names out, so a name missing from Fields may still be declared.
func (w *walk) unreadable() (names []string, cut bool) {
	fields := make(map[string]bool, len(w.fields))
	for _, f := range w.fields {
		fields[f.Name] = true
	}
	for _, n := range w.unread.order {
		if !fields[n] {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	cut = w.fieldsCut || w.cut > 0 || w.unread.cut || len(names) > maxListedSetNames
	return names[:min(len(names), maxListedSetNames)], cut
}

// setInPlaybook lists the names set in the playbook that carry no marker
// anywhere, sorted, up to maxListedSetNames.
func (w *walk) setInPlaybook() []string {
	names := []string{}
	for _, n := range w.set.order {
		if !w.marked.has(n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if len(names) > maxListedSetNames || w.set.cut || w.marked.cut {
		w.addNotes(hint(codeTooManySetNames, w.path, 0, "more than %d variables are set in this playbook; the first %d (sorted) are listed, past %d none are recorded",
			maxListedSetNames, maxListedSetNames, maxSetNames))
		names = names[:min(len(names), maxListedSetNames)]
	}
	return names
}

// nameSet is a set of variable names in first-seen order, up to
// maxSetNames. Only names a field could have are kept.
type nameSet struct {
	seen  map[string]bool
	order []string
	cut   bool
}

func newNameSet() *nameSet { return &nameSet{seen: map[string]bool{}} }

func (s *nameSet) add(name string) {
	if s.seen[name] || !schema.ValidFieldName(name) {
		return
	}
	if len(s.order) == maxSetNames {
		s.cut = true
		return
	}
	s.seen[name] = true
	s.order = append(s.order, name)
}

func (s *nameSet) addAll(o *nameSet) {
	for _, n := range o.order {
		s.add(n)
	}
	s.cut = s.cut || o.cut
}

func (s *nameSet) has(name string) bool { return s.seen[name] }
