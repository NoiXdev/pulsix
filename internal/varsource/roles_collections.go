// Roles inside collections (backlog 7.96): the collections keyword and the
// collection-role lookup, read against ansible-core 2.21.5
// (RoleDefinition._load_role_path, _get_collection_role_path,
// AnsibleCollectionRef, the collection finder) and checked with
// ansible-playbook --syntax-check.
//
// A role name is tried as a collection role first: a fully qualified name
// (namespace.collection[.subdirs].role) in its own collection only, any
// other name in each collection of the collections list that applies to
// the use, in order. A collection is looked for in the collections
// directory beside the playbook the walk started with (ansible-playbook
// adds no imported playbook's), then in the installed collections; the
// first that has <namespace>/<collection> as a directory is the
// collection, the later ones are not looked at for it. The role is
// <collection>/roles/<path> as a directory. Not found there, the name goes
// on to the role search.
//
// Left out, as for the role search: the worker's own collections path
// (outside the repository and the install), a collection's
// meta/runtime.yml routing, and the default collection of a playbook that
// is itself inside a collection.
package varsource

import (
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// adjacentCollections is the collections directory beside a playbook, in
// the repository.
const adjacentCollections = "collections/ansible_collections"

// collectionLists interns the collections lists of one walk: a role use
// carries its list's id, so its keys stay small however long the list is.
// Id 0 is no list (none given, or an empty one).
type collectionLists struct {
	ids   map[string]int
	lists [][]string
}

func newCollectionLists() *collectionLists {
	return &collectionLists{ids: map[string]int{}, lists: [][]string{nil}}
}

// intern is list's id.
func (c *collectionLists) intern(list []string) int {
	if len(list) == 0 {
		return 0
	}
	var b strings.Builder
	for _, name := range list {
		b.WriteString(strconv.Itoa(len(name)))
		b.WriteByte(':')
		b.WriteString(name)
	}
	key := b.String()
	if id, ok := c.ids[key]; ok {
		return id
	}
	id := len(c.lists)
	c.ids[key] = id
	c.lists = append(c.lists, list)
	return id
}

func (c *collectionLists) get(id int) []string { return c.lists[id] }

// collections reads a play's, block's or task's collections keyword
// against the list it would inherit (ansible-core 2.21.5, checked with
// ansible-playbook): an absent key keeps inherited, and so does a null
// value on a task or play; on a block a null value empties the list
// (Block._load_collections). A name is a list of one, and a list of names,
// an empty one too, replaces inherited; a name listed twice counts once,
// where it is first. Any other value fails the run; it is noted, and
// inherited is kept.
func (f *playFile) collections(n *yaml.Node, inherited int, block bool) int {
	if n == nil {
		return inherited
	}
	r := resolve(n)
	if isNull(r) {
		if block {
			return 0
		}
		return inherited
	}
	if name, ok := f.y.scalarText(r); ok {
		return f.lists.intern([]string{name})
	}
	if r.Kind == yaml.SequenceNode {
		var out []string
		seen := map[string]bool{}
		ok := true
		for _, it := range f.y.items(r) {
			name, isText := f.y.scalarText(it)
			if !isText {
				ok = false
				break
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		if ok && !f.y.exceeded() {
			return f.lists.intern(out)
		}
	}
	if !f.y.exceeded() {
		f.y.note(warning(codeCollectionsInvalid, f.y.file, r.Line,
			"collections must be a collection name or a list of them; the run fails on it, and the roles here are looked up with the list they would have without it"))
	}
	return inherited
}

// collectionLookup is what the collection-role lookup found: the role, or
// for a fully qualified name its collection and, when the collection was
// found but holds no such role, where it was looked for.
type collectionLookup struct {
	found     *roleTarget
	qualified string   // a fully qualified name's collection; "" for a short name
	where     string   // where the role was looked for in collections that exist
	absent    []string // collections tried that are in neither place
}

// collectionRole looks name up as a collection role under the collections
// list colls. Every collection tried costs the walk a step, so a long list
// used by many roles ends in the walk's limits rather than running on.
func (w *walk) collectionRole(name string, colls []string) collectionLookup {
	var l collectionLookup
	rel, list := "", colls
	if coll, sub, ok := qualifiedRole(name); ok {
		l.qualified, rel, list = coll, sub, []string{coll}
	} else {
		if len(colls) == 0 {
			return l
		}
		if rel, ok = shortRolePath(name); !ok {
			return l
		}
	}
	var tried []string
	for _, coll := range list {
		if !w.chargeSteps(1) {
			return collectionLookup{}
		}
		if !validCollectionName(coll) {
			continue
		}
		fsys, base, from, ok := w.collectionDir(coll)
		if !ok {
			if coll != "ansible.builtin" && coll != "ansible.legacy" {
				l.absent = append(l.absent, coll)
			}
			continue
		}
		p := path.Join(base, "roles", rel)
		if info, err := fs.Stat(fsys, p); err == nil && info.IsDir() {
			l.found = &roleTarget{state: RoleRead, dir: p, from: from}
			return l
		}
		_, prefix := w.tree(from)
		tried = append(tried, prefix+p)
	}
	if len(tried) > 0 {
		what := "in the collections at "
		if l.qualified != "" {
			what = "in collection " + l.qualified + " at "
		}
		l.where = what + strings.Join(tried, " or ")
	}
	return l
}

// collectionDir finds the collection coll (namespace.name), once per walk:
// the first of the collections beside the walk's playbook and the
// installed collections that has it as a directory. base is its path in
// fsys. ansible.builtin is never on disk: Ansible's loader gives it no
// search path.
func (w *walk) collectionDir(coll string) (fsys fs.FS, base string, from roleSource, ok bool) {
	if d, seen := w.collDirs[coll]; seen {
		fsys, _ = w.tree(d.from)
		return fsys, d.base, d.from, d.ok
	}
	d := w.findCollectionDir(coll)
	w.collDirs[coll] = d
	fsys, _ = w.tree(d.from)
	return fsys, d.base, d.from, d.ok
}

// collectionAt is where a collection is, if anywhere.
type collectionAt struct {
	base string
	from roleSource
	ok   bool
}

func (w *walk) findCollectionDir(coll string) collectionAt {
	if coll == "ansible.builtin" {
		return collectionAt{}
	}
	namespace, name, _ := strings.Cut(coll, ".")
	adjacent := path.Join(path.Dir(w.path), adjacentCollections, namespace, name)
	if isDir(w.p.fsys, adjacent) {
		return collectionAt{base: adjacent, from: fromRepo, ok: true}
	}
	if w.p.collections != nil && isDir(w.p.collections, path.Join(namespace, name)) {
		return collectionAt{base: path.Join(namespace, name), from: fromInstalledCollections, ok: true}
	}
	return collectionAt{}
}

// chargeSteps adds n steps to the walk and reports whether it is still
// within its limits; past them the walk is given up.
func (w *walk) chargeSteps(n int) bool {
	w.steps += n
	if w.steps > maxWalkSteps {
		w.giveUp(fmt.Sprintf("reading it with its imports and roles took more than %d steps or %d bytes", maxWalkSteps, maxWalkReadBytes))
	}
	return w.over == ""
}

func isDir(fsys fs.FS, p string) bool {
	info, err := fs.Stat(fsys, p)
	return err == nil && info.IsDir()
}

// qualifiedRole parses a fully qualified role name the way
// AnsibleCollectionRef.try_parse_fqcr does: three or more dotted parts of
// word characters whose first two are a collection name. The role's path
// in the collection's roles directory is the rest, one directory per part.
func qualifiedRole(name string) (collection, rel string, ok bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return "", "", false
	}
	for _, p := range parts {
		if p == "" || strings.IndexFunc(p, func(r rune) bool { return !isWordRune(r) }) >= 0 {
			return "", "", false
		}
	}
	collection = parts[0] + "." + parts[1]
	if !validCollectionName(collection) {
		return "", "", false
	}
	return collection, strings.Join(parts[2:], "/"), true
}

// shortRolePath is a short name's path in a collection's roles directory:
// the collection finder imports it as a module path, so each dotted part
// is a directory. A part that is empty, or a path segment that is empty,
// "." or "..", is not followed (Ansible's import of it fails or would
// leave the roles directory).
func shortRolePath(name string) (string, bool) {
	rel := strings.ReplaceAll(name, ".", "/")
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return rel, true
}

// validCollectionName is AnsibleCollectionRef.is_valid_collection_name:
// two dotted parts, each a Python identifier that is no keyword.
func validCollectionName(c string) bool {
	parts := strings.Split(c, ".")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if !isIdentifier(p) || pythonKeywords[p] {
			return false
		}
	}
	return true
}

// isWordRune is a character Python's \w matches in a str pattern:
// str.isalnum (a letter or any number) or the underscore.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// isIdentifier approximates str.isidentifier by the Unicode categories of
// XID_Start (letters, letter numbers, the underscore) and XID_Continue
// (also decimal digits, marks and connector punctuation). Names of real
// collections are ASCII; the approximation only matters for others.
func isIdentifier(s string) bool {
	for i, r := range s {
		start := r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
		if i == 0 && !start {
			return false
		}
		if !start && !unicode.In(r, unicode.Nd, unicode.Mn, unicode.Mc, unicode.Pc) {
			return false
		}
	}
	return s != ""
}

// pythonKeywords is keyword.kwlist of Python 3.13 (soft keywords are not
// in it).
var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true, "async": true,
	"await": true, "break": true, "class": true, "continue": true, "def": true, "del": true, "elif": true,
	"else": true, "except": true, "finally": true, "for": true, "from": true, "global": true, "if": true,
	"import": true, "in": true, "is": true, "lambda": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "return": true, "try": true, "while": true, "with": true, "yield": true,
}
