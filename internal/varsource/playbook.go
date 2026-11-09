package varsource

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// RoleState says how a role the playbook uses was read.
type RoleState string

// Role states. Only RoleRead and RoleNoSpecs mean the role's declarations
// are known; every other state leaves them unknown.
const (
	RoleRead       RoleState = "read"
	RoleNoSpecs    RoleState = "no_specs"
	RoleNotFound   RoleState = "not_found"
	RoleUnreadable RoleState = "unreadable"
	RoleDynamic    RoleState = "dynamic"
	RoleCollection RoleState = "collection"
	RoleInline     RoleState = "inline"
)

// RoleUse is one role (and entry point) a playbook or its imports use, in
// first-use order. Name is as written in the playbook.
type RoleUse struct {
	Name       string    `json:"name"`
	EntryPoint string    `json:"entry_point"`
	State      RoleState `json:"state"`
}

// FileState says how a playbook file or sidecar was read.
type FileState string

// File states. Only FileRead means the file's declarations are known.
const (
	FileRead       FileState = "read"
	FileMissing    FileState = "missing"
	FileUnreadable FileState = "unreadable"
	FileDynamic    FileState = "dynamic"
	FileCycle      FileState = "cycle"
	FileTooDeep    FileState = "too_deep"
	FileOutside    FileState = "outside"
)

// FileUse is one playbook file (the playbook and each import) or present
// sidecar, in the order the walk reached it. A file imported again on the
// import path of itself appears a second time as FileCycle.
type FileUse struct {
	Path  string    `json:"path"`
	State FileState `json:"state"`
}

// Playbook is the parsed variable sources of one playbook with its imports
// and roles (spec 7.75 section 6).
type Playbook struct {
	Path   string         `json:"path"`
	Failed bool           `json:"failed"` // the playbook was not read; nothing from it counts
	Fields []schema.Field `json:"fields"` // precedence order, first wins
	// FieldsCut: a cap left declared top-level fields out (more than 100
	// options in an entry point or in all, more than 100 prompts), so a
	// name missing from Fields may still be declared.
	FieldsCut bool `json:"fields_cut"`
	// Unreadable names variables a read source declares but that map onto
	// no field (an unreadable marker, an option of an unsupported type…):
	// declared, not removed. Sorted; names outside the field name rule are
	// left out.
	Unreadable    []string  `json:"unreadable,omitempty"`
	SetInPlaybook []string  `json:"set_in_playbook"`
	Roles         []RoleUse `json:"roles"`
	Files         []FileUse `json:"files"`
	Notes         []Note    `json:"notes"`
}

// Limits of the repo read (spec 7.75 constraints).
const (
	MaxPlaybooks   = 1000
	MaxFileBytes   = 512 << 10
	MaxImportDepth = 3
	MaxReportBytes = 8 << 20
)

// Limits of one playbook's walk, its imports and roles together. They are
// far above what a real playbook needs; past one the playbook counts as not
// read (playbook_too_complex).
const (
	maxWalkFiles      = 128              // playbook files read
	maxWalkRoles      = 512              // distinct roles (and entry points) used
	maxWalkBytes      = 16 << 20         // bytes of files read
	maxWalkSteps      = 4 * maxVisits    // mapping entries and sequence items visited
	maxWalkReadBytes  = 4 * maxReadBytes // scalar bytes read
	maxSetNames       = 10_000           // distinct names recorded as set or marked
	maxListedSetNames = 1000             // names listed in SetInPlaybook
	maxRefBytes       = 256              // a role name, entry point or import path kept
)

// inlinePath is the file name of an inline playbook's notes and sources.
const inlinePath = "inline"

// PlaybookPathPattern is a repo playbook's path: letters, digits, dots,
// slashes and hyphens, at most maxPlaybookPathStem of them, ending in .yml
// or .yaml. A path must also not contain "..".
var PlaybookPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}\.ya?ml$`)

// maxPlaybookPathStem is PlaybookPathPattern's bound on a path before its
// extension. The manager keys the stored sources by path, so the bound
// also keeps a path well inside what it can index.
const maxPlaybookPathStem = 200

func validPlaybookPath(p string) bool {
	return !strings.Contains(p, "..") && PlaybookPathPattern.MatchString(p)
}

// unsupportedPath is the warning for a path no playbook may have, false
// for a valid one: path_too_long past maxPlaybookPathStem (the path
// clipped in the note), path_unsupported otherwise.
func unsupportedPath(p string) (Note, bool) {
	if validPlaybookPath(p) {
		return Note{}, false
	}
	if stem := strings.TrimSuffix(strings.TrimSuffix(p, ".yml"), ".yaml"); len(stem) > maxPlaybookPathStem {
		return warning(codePathTooLong, clip(p, maxRefBytes), 0,
			"a playbook path is at most %d characters before its extension, this one has %d; not read", maxPlaybookPathStem, len(stem)), true
	}
	return warning(codePathUnsupported, p, 0,
		"a playbook path is letters, digits, dots, slashes and hyphens ending in .yml or .yaml, without \"..\"; not read"), true
}

// ParsePlaybook reads the playbook at path in fsys (the repo subpath) with
// its sidecar, imports and roles. It never fails: a problem is a note, and
// a playbook it cannot read is Failed.
func ParsePlaybook(fsys fs.FS, path string) Playbook {
	if note, bad := unsupportedPath(path); bad {
		return failedPlaybook(path, FileUnreadable, note)
	}
	data, err := readLimited(fsys, path)
	if err != nil {
		return failedPlaybook(path, stateOf(err), readNote(path, err))
	}
	d := decodePlaybook(path, data)
	if !d.ok {
		return failedPlaybook(path, FileUnreadable, d.notes...)
	}
	return (&parser{fsys: fsys}).parse(path, d, len(data))
}

func failedPlaybook(p string, state FileState, notes ...Note) Playbook {
	return Playbook{Path: p, Failed: true, Fields: []schema.Field{}, SetInPlaybook: []string{}, Roles: []RoleUse{},
		Files: []FileUse{{Path: p, State: state}}, Notes: notes}
}

// parser reads playbooks from one fsys; fsys is nil for an inline playbook,
// which has no repository to find roles, sidecars or imports in. roles,
// when not nil, holds the roles installed from requirements.yml.
type parser struct {
	fsys        fs.FS
	roles       fs.FS
	collections fs.FS
}

func (p *parser) inline() bool { return p.fsys == nil }

// parse walks a decoded playbook of size bytes.
func (p *parser) parse(file string, d decoded, size int) Playbook {
	w := &walk{
		p:        p,
		path:     file,
		declared: map[string]schema.FieldSource{},
		set:      newNameSet(),
		marked:   newNameSet(),
		visited:  map[string]bool{},
		onStack:  map[string]bool{},
		fileSeen: map[FileUse]bool{},
		roleSeen: map[roleKey]bool{},
		unread:   newNameSet(),
		specs:    map[specKey]*roleSpec{},
		charged:  map[*yamlFile][2]int{},
		targets:  map[targetKey]roleTarget{},
		lists:    newCollectionLists(),
		collDirs: map[string]collectionAt{},
		rawBytes: size,
	}
	read := w.visit(file, d, 0)
	return w.finish(read)
}

// noteSource is one file's notes in walk order: a playbook file's yamlFile
// (it gathers notes until the walk ends) or a list already taken.
type noteSource struct {
	y     *yamlFile
	notes []Note
}

// walk is the read of one playbook with its imports and roles.
type walk struct {
	p                                     *parser
	path                                  string
	files                                 []FileUse
	roles                                 []RoleUse
	sources                               []noteSource
	fields                                []schema.Field
	declared                              map[string]schema.FieldSource // every name declared so far, kept or not
	cut                                   int                           // distinct names past schema.MaxFields
	firstCut                              string
	set                                   *nameSet
	marked                                *nameSet
	unread                                *nameSet // declared names mapped onto no field
	fieldsCut                             bool     // a source's own cap cut top-level fields
	specs                                 map[specKey]*roleSpec
	charged                               map[*yamlFile][2]int // work of each file already accounted
	visited                               map[string]bool      // playbook files loaded
	onStack                               map[string]bool      // playbook files on the current import path
	fileSeen                              map[FileUse]bool
	roleSeen                              map[roleKey]bool
	targets                               map[targetKey]roleTarget // where each role name leads
	lists                                 *collectionLists         // the collections lists of the walk's files
	collDirs                              map[string]collectionAt  // where each collection is
	filesRead, rawBytes, steps, readBytes int
	over                                  string // why the walk was given up; "" while it holds
}

// visit reads one playbook file of the walk, then its sidecar, its roles
// and its imports. False when the file itself could not be read.
func (w *walk) visit(file string, d decoded, depth int) bool {
	w.visited[file] = true
	w.filesRead++
	f := readPlayFile(d.y, d.doc, path.Dir(file), w.lists)
	w.account(d.y)
	if n, over := d.y.tooComplex(); over {
		w.useFile(file, FileUnreadable)
		w.addNotes(n)
		return false
	}
	w.useFile(file, FileRead)
	w.sources = append(w.sources, noteSource{y: d.y})
	w.set.addAll(f.set)
	w.marked.addAll(f.marked)
	w.unread.addAll(f.unread)
	w.fieldsCut = w.fieldsCut || f.cut
	if f.overflow {
		w.giveUp(fmt.Sprintf("%s uses more than %d roles or imports", file, maxWalkRoles))
		return true
	}
	w.onStack[file] = true
	defer delete(w.onStack, file)
	w.readSidecar(file)
	w.merge(f.markers)
	w.merge(f.prompts)
	w.useRoles(f)
	w.followImports(f, depth)
	return true
}

func (w *walk) useFile(p string, state FileState) {
	u := FileUse{Path: clip(p, maxRefBytes), State: state}
	if !w.fileSeen[u] {
		w.fileSeen[u] = true
		w.files = append(w.files, u)
	}
}

func (w *walk) addNotes(notes ...Note) {
	if len(notes) > 0 {
		w.sources = append(w.sources, noteSource{notes: notes})
	}
}

// account adds a read file's work since its last account to the walk's
// and gives the walk up past its limits.
func (w *walk) account(y *yamlFile) {
	last := w.charged[y]
	w.steps += y.visits() - last[0]
	w.readBytes += y.bytesRead() - last[1]
	w.charged[y] = [2]int{y.visits(), y.bytesRead()}
	if w.steps > maxWalkSteps || w.readBytes > maxWalkReadBytes {
		w.giveUp(fmt.Sprintf("reading it with its imports and roles took more than %d steps or %d bytes", maxWalkSteps, maxWalkReadBytes))
	}
}

func (w *walk) giveUp(why string) {
	if w.over == "" {
		w.over = why
	}
}

// readSidecar reads the playbook file's sidecar, <name>.argument_specs.yml
// (or .yaml) next to it, when present.
func (w *walk) readSidecar(file string) {
	if w.p.inline() {
		return
	}
	base := strings.TrimSuffix(file, path.Ext(file))
	for _, ext := range []string{".yml", ".yaml"} {
		name := base + ".argument_specs" + ext
		data, err := w.read(w.p.fsys, name)
		switch {
		case isNotExist(err):
			continue
		case errors.Is(err, errGivenUp):
			return
		case err != nil:
			w.useFile(name, FileUnreadable)
			w.addNotes(readNote(name, err))
			return
		}
		y := newYAMLFile(name)
		res := readSpecData(y, data, "", "main", schema.SourceSidecar)
		w.account(y)
		w.addNotes(res.notes...)
		if !res.readOK || res.broken {
			w.useFile(name, FileUnreadable)
			return
		}
		w.useFile(name, FileRead)
		w.addSpecResult(res)
		return
	}
}

// addSpecResult merges an entry point's fields and records what it could
// not map or cut.
func (w *walk) addSpecResult(res specResult) {
	w.fieldsCut = w.fieldsCut || res.cut
	for _, n := range res.unreadable {
		w.unread.add(n)
	}
	w.merge(res.fields)
}

// merge adds fields in precedence order: a name declared before keeps its
// first declaration (the later one is a duplicate_name hint), and past
// schema.MaxFields names the rest are cut.
func (w *walk) merge(fields []schema.Field) {
	for _, f := range fields {
		if first, dup := w.declared[f.Name]; dup {
			w.addNotes(hint(codeDuplicateName, f.Source.File, f.Source.Line,
				"%s is also declared at %s:%d, which comes first; this declaration is not used", f.Name, clip(first.File, maxRefBytes), first.Line))
			continue
		}
		w.declared[f.Name] = *f.Source
		if len(w.fields) == schema.MaxFields {
			if w.cut == 0 {
				w.firstCut = f.Name
			}
			w.cut++
			continue
		}
		w.fields = append(w.fields, f)
	}
}

// finish builds the result. A walk given up, or a playbook file not read,
// is Failed: nothing from it counts.
func (w *walk) finish(read bool) Playbook {
	pb := Playbook{Path: w.path, Fields: []schema.Field{}, SetInPlaybook: []string{}, Roles: w.roles, Files: w.files}
	switch {
	case w.over != "":
		pb.Failed = true
		w.addNotes(warning(codePlaybookTooComplex, w.path, 0, "%s; nothing read from this playbook", w.over))
	case !read:
		pb.Failed = true
	default:
		if w.cut > 0 {
			w.addNotes(warning(codeTooManyFields, w.path, 0, "this playbook declares more than %d variables (imports and roles included); %s and %d more are left out",
				schema.MaxFields, w.firstCut, w.cut-1))
		}
		pb.SetInPlaybook = w.setInPlaybook()
		for _, f := range w.fields {
			f.SetInPlaybook = w.set.has(f.Name) && !w.marked.has(f.Name)
			pb.Fields = append(pb.Fields, f)
		}
		pb.Unreadable, pb.FieldsCut = w.unreadable()
	}
	var notes []Note
	for _, s := range w.sources {
		if s.y != nil {
			if n, over := s.y.tooComplex(); over {
				notes = append(notes, n) // a spec file that ran out of budget on a later entry point
			} else {
				notes = append(notes, s.y.takeNotes()...)
			}
		} else {
			notes = append(notes, s.notes...)
		}
	}
	pb.Notes = capNotes(notes, w.path, maxNotesPerList)
	if pb.Roles == nil {
		pb.Roles = []RoleUse{}
	}
	return pb
}
