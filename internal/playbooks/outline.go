// Playbook outline: a read-only structural summary of a playbook's plays,
// roles and tasks, for the editor's outline view and the dry-run tag picker.
package playbooks

import (
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// maxOutlineEntries is the work budget of one outline: every play and
	// every visited list item (used or skipped, aliased or not) is charged
	// against it, so neither the walk's time nor the result's size can grow
	// with the input. Past it the walk stops and the outline is marked
	// truncated.
	maxOutlineEntries = 2000
	// maxOutlineDepth bounds how deep block/rescue/always nest.
	maxOutlineDepth = 64
	// maxOutlineTagsPerList bounds the tags read from one tags: list and an
	// entry's effective (own plus inherited) tag set; maxOutlineTags bounds
	// the top-level union.
	maxOutlineTagsPerList = 64
	maxOutlineTags        = 512
	// maxOutlineTagsTotal bounds the tags emitted across the whole outline
	// (every play's and entry's list together), so the JSON stays near
	// 1 MiB at the worst case of maxOutlineText bytes per tag. Up to half of
	// it, entries carry their inherited tags; past that only their own,
	// and past the whole budget none.
	maxOutlineTagsTotal = 3840
	// maxOutlineTagsText bounds how much of one tags string (or list item)
	// is split into tags; it is cut by length before it is scanned.
	maxOutlineTagsText = maxOutlineTagsPerList * (maxOutlineText + 1)
	// maxOutlineText bounds every string the outline emits (names, hosts,
	// import paths, tags), in bytes.
	maxOutlineText = 256
)

// Outline sections, in the order a play runs them (handlers last).
const (
	SectionPreTasks  = "pre_tasks"
	SectionRoles     = "roles"
	SectionTasks     = "tasks"
	SectionPostTasks = "post_tasks"
	SectionHandlers  = "handlers"
)

// Outline entry kinds.
const (
	KindTask    = "task"
	KindBlock   = "block"
	KindInclude = "include"
	KindImport  = "import"
	KindRole    = "role"
)

// PlaybookOutline is the structure of one playbook document (the type is
// not named Outline: that is the function producing it).
type PlaybookOutline struct {
	Plays     []OutlinePlay `json:"plays"`
	Tags      []string      `json:"tags"`
	Counts    OutlineCounts `json:"counts"`
	Truncated bool          `json:"truncated"`
}

// OutlinePlay is one play. A top-level import_playbook is a play-like entry
// carrying the imported path as its name and no entries.
type OutlinePlay struct {
	Name    string         `json:"name"`
	Hosts   string         `json:"hosts"`
	Become  bool           `json:"become"`
	Line    int            `json:"line"`
	Tags    []string       `json:"tags"`
	Entries []OutlineEntry `json:"entries"`
}

// OutlineEntry is one task, block, include/import or role of a play.
// Tags are the entry's own plus those inherited from the play and enclosing
// blocks; RunsInCheckMode is false when the entry may run for real in a dry
// run: its own check_mode, else the nearest enclosing block's or play's, is
// set to anything but literally true (false, a template, …).
type OutlineEntry struct {
	Section         string   `json:"section"`
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	Line            int      `json:"line"`
	Depth           int      `json:"depth"`
	Tags            []string `json:"tags"`
	RunsInCheckMode bool     `json:"runs_in_check_mode"`
}

// OutlineCounts summarises an outline: plays (including imported
// playbooks), tasks (task/include/import entries outside handlers; blocks and
// roles are not counted) and handlers.
type OutlineCounts struct {
	Plays    int `json:"plays"`
	Tasks    int `json:"tasks"`
	Handlers int `json:"handlers"`
}

// taskKeywords are the task/block keys that are not a module name; with_*
// loops are matched by prefix.
var taskKeywords = map[string]bool{
	"name": true, "tags": true, "when": true, "vars": true, "register": true, "loop": true,
	"loop_control": true, "become": true, "become_user": true, "become_method": true,
	"become_flags": true, "check_mode": true, "diff": true, "delegate_to": true,
	"delegate_facts": true, "environment": true, "any_errors_fatal": true,
	"async": true, "poll": true, "ignore_errors": true, "ignore_unreachable": true, "no_log": true, "run_once": true,
	"changed_when": true, "failed_when": true, "notify": true, "listen": true, "args": true,
	"until": true, "retries": true, "delay": true, "throttle": true, "timeout": true,
	"connection": true, "remote_user": true, "port": true, "collections": true,
	"module_defaults": true, "debugger": true, "block": true, "rescue": true, "always": true,
}

// includeKeys / importKeys map a task's module key (bare or in a
// collection namespace) to the entry kind.
var includeImportKinds = map[string]string{
	"include_tasks": KindInclude, "include_role": KindInclude, "include": KindInclude,
	"import_tasks": KindImport, "import_role": KindImport,
}

var outlineImportPlaybook = []string{"import_playbook", "ansible.builtin.import_playbook", "ansible.legacy.import_playbook"}

// Outline parses content (a playbook document, validated like ValidateYAML)
// into its structure. It never follows includes, imports or roles. The
// error wraps ErrInvalidInput.
func Outline(content []byte) (PlaybookOutline, error) {
	if err := ValidateYAML(string(content)); err != nil {
		return PlaybookOutline{}, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return PlaybookOutline{}, err
	}
	w := &outlineWalker{tagSet: map[string]bool{}}
	w.out.Plays = []OutlinePlay{}
	for _, item := range resolveAlias(doc.Content[0]).Content {
		if !w.spend() {
			break
		}
		w.play(resolveAlias(item))
	}
	w.out.Counts.Plays = len(w.out.Plays)
	w.out.Tags = sortedKeys(w.tagSet)
	return w.out, nil
}

type outlineWalker struct {
	out     PlaybookOutline
	total   int
	tagSet  map[string]bool
	tagsOut int // tags emitted so far, against maxOutlineTagsTotal
	section string
	entries []OutlineEntry
}

// spend charges one visited item against the shared budget; false (and the
// outline marked truncated) once it is used up.
func (w *outlineWalker) spend() bool {
	if w.total >= maxOutlineEntries {
		w.out.Truncated = true
		return false
	}
	w.total++
	return true
}

func (w *outlineWalker) play(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for _, key := range outlineImportPlaybook {
		if v, ok := lookup(n, key); ok {
			name := scalarText(v)
			if name == "" {
				name = KindImport
			}
			w.out.Plays = append(w.out.Plays, OutlinePlay{
				Name: w.clip(name), Line: n.Line, Tags: []string{}, Entries: []OutlineEntry{},
			})
			return
		}
	}
	p := OutlinePlay{Line: n.Line, Tags: w.effectiveTags(nil, n), Entries: []OutlineEntry{}}
	if v, ok := lookup(n, "hosts"); ok {
		p.Hosts = w.clip(hostsText(v))
	}
	p.Name = p.Hosts
	if v, ok := lookup(n, "name"); ok && scalarText(v) != "" {
		p.Name = w.clip(scalarText(v))
	}
	if v, ok := lookup(n, "become"); ok {
		p.Become = isTrue(v)
	}
	w.noteTags(p.Tags)
	ctx := entryContext{tags: p.Tags, check: inCheckMode(n, true)}
	w.entries = p.Entries
	for _, sec := range []string{SectionPreTasks, SectionRoles, SectionTasks, SectionPostTasks, SectionHandlers} {
		list, ok := lookup(n, sec)
		if !ok || list.Kind != yaml.SequenceNode {
			continue
		}
		w.section = sec
		for _, item := range list.Content {
			if !w.spend() {
				break
			}
			if sec == SectionRoles {
				w.role(resolveAlias(item), ctx)
			} else {
				w.task(resolveAlias(item), ctx, 0)
			}
		}
	}
	p.Entries = w.entries
	w.out.Plays = append(w.out.Plays, p)
}

// entryContext is what an entry inherits from its play and enclosing blocks.
type entryContext struct {
	tags  []string
	check bool
}

// add appends one entry; its item was already charged by the caller.
func (w *outlineWalker) add(e OutlineEntry) {
	if e.Name == "" {
		e.Name = e.Kind
	}
	e.Name = w.clip(e.Name)
	w.entries = append(w.entries, e)
	w.noteTags(e.Tags)
	switch {
	case e.Section == SectionHandlers && e.Kind != KindBlock && e.Kind != KindRole:
		w.out.Counts.Handlers++
	case e.Kind != KindBlock && e.Kind != KindRole:
		w.out.Counts.Tasks++
	}
}

func (w *outlineWalker) role(n *yaml.Node, ctx entryContext) {
	e := OutlineEntry{Section: w.section, Kind: KindRole, Line: n.Line, Tags: ctx.tags, RunsInCheckMode: ctx.check}
	switch n.Kind {
	case yaml.ScalarNode:
		e.Name = n.Value
	case yaml.MappingNode:
		e.Tags = w.effectiveTags(ctx.tags, n)
		e.RunsInCheckMode = inCheckMode(n, ctx.check)
		for _, key := range []string{"role", "name"} {
			if v, ok := lookup(n, key); ok && scalarText(v) != "" {
				e.Name = scalarText(v)
				break
			}
		}
	default:
		return
	}
	w.add(e)
}

func (w *outlineWalker) task(n *yaml.Node, ctx entryContext, depth int) {
	if n.Kind != yaml.MappingNode {
		return
	}
	e := OutlineEntry{
		Section: w.section, Kind: KindTask, Line: n.Line, Depth: depth,
		Tags:            w.effectiveTags(ctx.tags, n),
		RunsInCheckMode: inCheckMode(n, ctx.check),
	}
	name := ""
	if v, ok := lookup(n, "name"); ok {
		name = scalarText(v)
	}
	if blk, ok := lookup(n, "block"); ok {
		e.Kind = KindBlock
		e.Name = name
		if e.Name == "" {
			e.Name = "block"
		}
		w.add(e)
		if depth >= maxOutlineDepth {
			w.out.Truncated = true
			return
		}
		child := entryContext{tags: e.Tags, check: e.RunsInCheckMode}
		for _, part := range []*yaml.Node{blk, lookupOrNil(n, "rescue"), lookupOrNil(n, "always")} {
			if part == nil || part.Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range part.Content {
				if !w.spend() {
					return
				}
				w.task(resolveAlias(item), child, depth+1)
			}
		}
		return
	}
	module, arg := moduleOf(n)
	if module == "action" || module == "local_action" {
		module = actionModule(arg)
	}
	if kind, ok := includeImportKinds[strings.TrimPrefix(strings.TrimPrefix(module, "ansible.builtin."), "ansible.legacy.")]; ok {
		e.Kind = kind
		if name == "" {
			name = includeTarget(arg)
		}
	}
	if name == "" {
		name = module
	}
	e.Name = name
	w.add(e)
}

// moduleOf returns a task's module key (the first key that is no keyword)
// and its value.
func moduleOf(n *yaml.Node) (string, *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind != yaml.ScalarNode || taskKeywords[k.Value] || strings.HasPrefix(k.Value, "with_") {
			continue
		}
		return k.Value, resolveAlias(n.Content[i+1])
	}
	return "", nil
}

// actionModule names the module an action: / local_action: runs: the first
// word of its string value, or the module key of its mapping.
func actionModule(arg *yaml.Node) string {
	if arg == nil {
		return ""
	}
	switch arg.Kind {
	case yaml.ScalarNode:
		if f := strings.Fields(scalarText(arg)); len(f) > 0 {
			return f[0]
		}
	case yaml.MappingNode:
		if v, ok := lookup(arg, "module"); ok {
			return scalarText(v)
		}
	}
	return ""
}

// includeTarget names what an include/import points at: the scalar
// argument, or the file / role / name / _raw_params of its mapping.
func includeTarget(arg *yaml.Node) string {
	if arg == nil {
		return ""
	}
	if arg.Kind == yaml.ScalarNode {
		return arg.Value
	}
	for _, key := range []string{"file", "name", "role", "_raw_params"} {
		if v, ok := lookup(arg, key); ok && scalarText(v) != "" {
			return scalarText(v)
		}
	}
	return ""
}
