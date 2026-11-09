package varsource

import (
	"fmt"
	"path"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"gopkg.in/yaml.v3"
)

// maxBlockDepth bounds how deep block/rescue/always nest; deeper, the file
// is given up (file_too_complex).
const maxBlockDepth = 64

// roleKeywords are the keys of a roles: entry that are not role parameters.
var roleKeywords = map[string]bool{
	"role": true, "name": true, "tags": true, "when": true, "vars": true, "become": true,
	"become_user": true, "become_method": true, "become_flags": true, "delegate_to": true,
	"delegate_facts": true, "environment": true, "any_errors_fatal": true, "ignore_errors": true,
	"ignore_unreachable": true, "no_log": true, "run_once": true, "check_mode": true, "diff": true,
	"collections": true, "connection": true, "debugger": true, "module_defaults": true, "port": true,
	"remote_user": true, "throttle": true, "timeout": true, "apply": true,
}

// roleModules are the task keys that use a role.
var roleModules = []string{
	"import_role", "include_role",
	"ansible.builtin.import_role", "ansible.builtin.include_role",
	"ansible.legacy.import_role", "ansible.legacy.include_role",
}

// importKeys are the play-list keys that import a playbook.
var importKeys = []string{"import_playbook", "ansible.builtin.import_playbook", "ansible.legacy.import_playbook"}

// roleRef is a role use as written: its name, entry point ("main" unless
// tasks_from names another), line and the collections list that applies to
// it (backlog 7.96). dynamic: tasks_from is a template; invalid: the use
// could not be read (no usable name or tasks_from), name is then whatever
// text it had.
type roleRef struct {
	name, entryPoint string
	line             int
	collections      int // the walk's collectionLists id
	dynamic, invalid bool
}

// importRef is an import_playbook target as written; invalid: the target
// is no text, and target stands in for it ("import_playbook@<line>").
type importRef struct {
	target  string
	line    int
	invalid bool
}

// playFile is what one playbook file declares: marker and prompt fields,
// role uses and imports in order, and the names it sets or marks.
type playFile struct {
	y        *yamlFile
	dir      string
	markers  []schema.Field
	prompts  []schema.Field
	refs     []roleRef
	imports  []importRef
	set      *nameSet
	marked   *nameSet
	unread   *nameSet            // marked variables that are no field (an unreadable marker, a value of no field type)
	cut      bool                // vars_prompt entries were left out by a cap
	overflow bool                // more role uses or imports than one walk reads
	seen     map[*yaml.Node]bool // vars mappings and prompt lists read
	refSeen  map[refKey]bool
	impSeen  map[string]bool
	lists    *collectionLists    // the walk's collections lists
	allowed  map[*yaml.Node]bool // nodes whose line comment was read as a marker
}

// readPlayFile reads the plays of one playbook file (doc is its parsed
// document, checked by isPlaybook). Its owner checks y.tooComplex after.
func readPlayFile(y *yamlFile, doc *yaml.Node, dir string, lists *collectionLists) *playFile {
	f := &playFile{y: y, dir: dir, set: newNameSet(), marked: newNameSet(), unread: newNameSet(), lists: lists,
		seen: map[*yaml.Node]bool{}, refSeen: map[refKey]bool{}, impSeen: map[string]bool{}, allowed: map[*yaml.Node]bool{}}
	for _, item := range y.items(documentRoot(doc)) {
		a := y.attrs(item)
		if target, ok := importTarget(a); ok {
			f.importEntry(target, a["vars"])
			continue
		}
		f.vars(a["vars"])
		f.varsPrompt(a["vars_prompt"])
		colls := f.collections(a["collections"], 0, false)
		f.tasks(a["pre_tasks"], 0, colls)
		f.roleEntries(a["roles"], colls)
		f.tasks(a["tasks"], 0, colls)
		f.tasks(a["post_tasks"], 0, colls)
		f.tasks(a["handlers"], 0, colls)
	}
	if !y.exceeded() {
		f.misplacedMarkers(doc)
	}
	return f
}

func importTarget(a map[string]*yaml.Node) (*yaml.Node, bool) {
	for _, k := range importKeys {
		if v, ok := a[k]; ok {
			return v, true
		}
	}
	return nil, false
}

// visitPairs is y.pairs charged on every visit: a mapping reached through
// many aliases costs its size each time it is walked.
func (y *yamlFile) visitPairs(n *yaml.Node) []pair {
	ps := y.pairs(n)
	if !y.spend(len(ps)) {
		return nil
	}
	return ps
}

// giveUp spends the file's budget: the file counts as not read.
func (y *yamlFile) giveUp() { y.left, y.over = 0, true }

// vars reads a play's vars: a marked entry is a field, any other is a name
// set in the playbook. A mapping reached again (an alias) is not read twice.
// A list of mappings (an older form Ansible still reads) is read item by
// item.
func (f *playFile) vars(n *yaml.Node) {
	if l := resolve(n); l != nil && l.Kind == yaml.SequenceNode {
		for _, it := range f.y.items(l) {
			if m := resolve(it); m != nil && m.Kind == yaml.MappingNode {
				f.vars(m)
			}
		}
		return
	}
	ps := f.y.visitPairs(n)
	if n = resolve(n); n == nil || f.seen[n] {
		return
	}
	f.seen[n] = true
	for _, p := range ps {
		field, notes, isMarker, ok := markedVar(f.y, p.key, p.value)
		f.y.note(notes...)
		if !isMarker {
			f.set.add(p.key.Value)
			continue
		}
		f.allowed[commentNode(p.key, p.value)] = true
		f.marked.add(p.key.Value)
		if ok {
			f.markers = append(f.markers, field)
		} else {
			f.unread.add(p.key.Value)
		}
	}
}

func (f *playFile) varsPrompt(n *yaml.Node) {
	if n = resolve(n); isNull(n) {
		return
	}
	if f.seen[n] {
		f.y.items(n) // charged, not read again
		return
	}
	f.seen[n] = true
	fields, notes, cut := promptFields(f.y, n)
	f.y.note(notes...)
	f.cut = f.cut || cut
	f.prompts = append(f.prompts, fields...)
}

// setKeys records the keys of a vars: mapping as set in the playbook.
func (f *playFile) setKeys(n *yaml.Node) {
	for _, p := range f.y.visitPairs(n) {
		f.set.add(p.key.Value)
	}
}

// roleEntries reads a play's roles: list. Keys of an entry other than the
// role keywords are role parameters, set in the playbook. colls is the
// play's collections list; an entry's own collections key is not one.
func (f *playFile) roleEntries(n *yaml.Node, colls int) {
	for _, it := range f.list(n, "roles") {
		e := resolve(it)
		if e == nil {
			continue
		}
		if e.Kind == yaml.MappingNode {
			for _, p := range f.y.visitPairs(e) {
				switch k := p.key.Value; {
				case k == "vars":
					f.setKeys(p.value)
				case !roleKeywords[k]:
					f.set.add(k)
				}
			}
			a := f.y.attrs(e)
			nameNode := a["role"]
			if nameNode == nil {
				nameNode = a["name"]
			}
			f.roleName(nameNode, nil, e.Line, colls)
			continue
		}
		f.roleName(e, nil, e.Line, colls)
	}
}

// tasks reads a task list for role uses, through block, rescue and always.
// colls is the collections list the tasks inherit; a block's or a task's
// own replaces it for what it holds.
func (f *playFile) tasks(n *yaml.Node, depth int, colls int) {
	if depth > maxBlockDepth {
		f.y.giveUp()
		return
	}
	for _, t := range f.list(n, "a task list") {
		a := f.y.attrs(t)
		_, isBlock := a["block"]
		for _, k := range []string{"rescue", "always"} {
			if _, ok := a[k]; ok {
				isBlock = true
			}
		}
		own := f.collections(a["collections"], colls, isBlock)
		nested := false
		for _, k := range []string{"block", "rescue", "always"} {
			if v, ok := a[k]; ok {
				nested = true
				f.tasks(v, depth+1, own)
			}
		}
		if nested {
			continue
		}
		for _, k := range roleModules {
			if args, ok := a[k]; ok {
				f.setKeys(a["vars"])
				f.roleTask(args, own)
				break
			}
		}
	}
}

// roleTask reads an import_role/include_role task's arguments: a mapping,
// or the key=value form ("name=db tasks_from=install").
func (f *playFile) roleTask(args *yaml.Node, colls int) {
	if args == nil {
		return
	}
	if args.Kind != yaml.ScalarNode || isNull(args) {
		a := f.y.attrs(args)
		f.roleName(a["name"], a["tasks_from"], args.Line, colls)
		return
	}
	text, _ := f.y.scalarText(args)
	kv := map[string]string{}
	for _, field := range strings.Fields(text) {
		if k, v, ok := strings.Cut(field, "="); ok {
			kv[k] = v
		}
	}
	name, ok := kv["name"]
	if !ok {
		f.invalidRole(text, args.Line, roleUseUnreadable)
		return
	}
	f.ref(name, kv["tasks_from"], args.Line, colls)
}

// roleName records a role use from a name node and an optional tasks_from
// node; line is where the entry starts, for a note when the name is
// missing.
func (f *playFile) roleName(nameNode, from *yaml.Node, line int, colls int) {
	name, ok := f.y.scalarText(nameNode)
	if !ok {
		f.invalidRole("", line, roleUseUnreadable)
		return
	}
	entry := ""
	if !isNull(from) {
		if entry, ok = f.y.scalarText(from); !ok {
			f.invalidRole(name, line, roleUseUnreadable)
			return
		}
	}
	f.ref(name, entry, resolve(nameNode).Line, colls)
}

const roleUseUnreadable = "a role use needs a role name (and tasks_from, if any, as text); not read"

// list is the items of a roles: or task list (tasks, pre_tasks,
// post_tasks, handlers, block, rescue, always). A value that is neither
// empty nor a list hides the role uses it may hold: it counts as one role
// use that cannot be read.
func (f *playFile) list(n *yaml.Node, what string) []*yaml.Node {
	r := resolve(n)
	if isNull(r) {
		return nil
	}
	if r.Kind != yaml.SequenceNode {
		f.invalidRole("", r.Line, what+" must be a list; nothing in it is read, and the roles it uses are unknown")
		return nil
	}
	return f.y.items(r)
}

// invalidRole records a role use that cannot be read, so the walk lists it
// as unreadable and its role's fields are not taken as gone. name is what
// text it had.
func (f *playFile) invalidRole(name string, line int, why string) {
	key := refKey{name: name, line: line, invalid: true}
	if f.overflow || f.refSeen[key] {
		return
	}
	f.y.note(warning(codeRoleInvalid, f.y.file, line, "%s", why))
	f.addRef(key, roleRef{name: name, entryPoint: "main", line: line, invalid: true})
}

// ref records a role use once per name, entry point and collections list.
// The entry point is tasks_from's file name without .yml/.yaml, as Ansible
// takes it.
func (f *playFile) ref(name, tasksFrom string, line int, colls int) {
	r := roleRef{name: name, entryPoint: "main", line: line, collections: colls}
	if tasksFrom != "" {
		if templated(tasksFrom) {
			r.entryPoint, r.dynamic = tasksFrom, true
		} else if ep := strings.TrimSuffix(strings.TrimSuffix(path.Base(tasksFrom), ".yml"), ".yaml"); ep != "" && ep != "." && ep != "/" {
			r.entryPoint = ep
		}
	}
	key := refKey{name: r.name, entryPoint: r.entryPoint, collections: colls}
	if f.overflow || f.refSeen[key] {
		return
	}
	f.addRef(key, r)
}

// refKey identifies a role use within a file: an invalid one by its line
// and text, any other by name, entry point and collections list.
type refKey struct {
	name, entryPoint string
	collections      int
	line             int
	invalid          bool
}

func (f *playFile) addRef(key refKey, r roleRef) {
	if len(f.refs) == maxWalkRoles {
		f.overflow = true
		return
	}
	f.refSeen[key] = true
	f.refs = append(f.refs, r)
}

// importEntry records an import_playbook target; keys of the entry's vars:
// are set in the playbook.
func (f *playFile) importEntry(target, vars *yaml.Node) {
	f.setKeys(vars)
	ref := importRef{line: target.Line}
	text, ok := f.y.scalarText(target)
	if ok {
		ref.target = text
	} else {
		ref.target, ref.invalid = fmt.Sprintf("import_playbook@%d", target.Line), true
	}
	if f.overflow || f.impSeen[ref.target] {
		return
	}
	if len(f.imports) == maxWalkFiles {
		f.overflow = true
		return
	}
	if ref.invalid {
		f.y.note(warning(codeImportInvalid, f.y.file, target.Line, "import_playbook needs a file name; not read"))
	}
	f.impSeen[ref.target] = true
	f.imports = append(f.imports, ref)
}

// misplacedMarkers warns about every "# pulsix:" comment line that is not
// the line comment of a play variable. It walks the document as written
// (aliases not followed), so its work is bounded by the file's size. The
// line is that of the node the comment belongs to.
func (f *playFile) misplacedMarkers(doc *yaml.Node) {
	stack := []*yaml.Node{doc}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		f.checkComment(n, n.HeadComment)
		if !f.allowed[n] {
			f.checkComment(n, n.LineComment)
		}
		f.checkComment(n, n.FootComment)
		if n.Kind == yaml.AliasNode {
			continue
		}
		for i := len(n.Content) - 1; i >= 0; i-- {
			stack = append(stack, n.Content[i])
		}
	}
}

func (f *playFile) checkComment(n *yaml.Node, comment string) {
	if !strings.Contains(comment, markerPrefix) {
		return
	}
	for _, line := range strings.Split(comment, "\n") {
		if _, isMarker, _ := parseMarker(strings.TrimSpace(line)); isMarker {
			f.y.note(warning(codeMarkerMisplaced, f.y.file, n.Line,
				`a "# pulsix:" marker counts only as the line comment of a variable in a play's vars:; this one is ignored`))
		}
	}
}
