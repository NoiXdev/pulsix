package varsource

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// collectionPlaybook is an import_playbook target naming a playbook of a
// collection (namespace.collection.playbook), which the repo does not hold.
var collectionPlaybook = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_.]+$`)

// followImports reads a playbook file's imports in order, depth-first: an
// imported playbook's sources rank after its importer's, and before the
// importer's next import.
func (w *walk) followImports(f *playFile, depth int) {
	for _, imp := range f.imports {
		if w.over != "" {
			return
		}
		w.follow(f, imp, depth+1)
	}
}

// follow reads one import at depth (1 for the playbook's own imports).
// Paths are relative to the importing file and stay inside the repo; a
// file on the import path of itself is a cycle; a file the walk loaded
// before adds nothing.
func (w *walk) follow(f *playFile, imp importRef, depth int) {
	y, raw := f.y, imp.target
	if imp.invalid {
		w.useFile(raw, FileUnreadable) // noted where it was read
		return
	}
	if w.p.inline() {
		w.useFile(raw, FileDynamic)
		y.note(hint(codeInlineImportUnresolved, y.file, imp.line, "an inline playbook has no repository to find %q in; its variables are not read", short(raw)))
		return
	}
	if templated(raw) {
		w.useFile(raw, FileDynamic)
		y.note(hint(codeImportDynamic, y.file, imp.line, "the imported playbook %q is a template the run evaluates; its variables cannot be read", short(raw)))
		return
	}
	target := path.Join(f.dir, raw)
	switch {
	case strings.HasPrefix(raw, "/"):
		w.useFile(raw, FileOutside)
		y.note(warning(codeImportOutside, y.file, imp.line, "%q is an absolute path, outside the repository; its variables are not read", short(raw)))
		return
	case target == ".." || strings.HasPrefix(target, "../"):
		w.useFile(target, FileOutside)
		y.note(warning(codeImportOutside, y.file, imp.line, "%q is outside the repository; its variables are not read", short(raw)))
		return
	case path.Ext(raw) != ".yml" && path.Ext(raw) != ".yaml" && collectionPlaybook.MatchString(raw):
		w.useFile(raw, FileOutside)
		y.note(warning(codeImportOutside, y.file, imp.line, "%q is a collection's playbook, which is not part of the repository; its variables are not read", short(raw)))
		return
	case !validPlaybookPath(target):
		w.useFile(target, FileUnreadable)
		y.note(warning(codePathUnsupported, y.file, imp.line,
			"%q: a playbook path is letters, digits, dots, slashes and hyphens ending in .yml or .yaml; not read", short(raw)))
		return
	case w.onStack[target]:
		w.useFile(target, FileCycle)
		y.note(warning(codeImportCycle, y.file, imp.line, "%s imports itself (through this import); the cycle is cut here", short(target)))
		return
	case w.visited[target]:
		return
	case depth > MaxImportDepth:
		w.useFile(target, FileTooDeep)
		y.note(warning(codeImportTooDeep, y.file, imp.line, "%s is imported more than %d levels below the playbook; its variables are not read", short(target), MaxImportDepth))
		return
	case w.filesRead == maxWalkFiles:
		w.giveUp(fmt.Sprintf("it imports more than %d playbook files", maxWalkFiles-1))
		return
	}
	w.visited[target] = true
	data, err := w.read(w.p.fsys, target)
	switch {
	case errors.Is(err, errGivenUp):
		return
	case isNotExist(err):
		w.useFile(target, FileMissing)
		y.note(warning(codeImportNotFound, y.file, imp.line, "%s not found; its variables are not read", short(target)))
		return
	case err != nil:
		w.useFile(target, FileUnreadable)
		w.addNotes(readNote(target, err))
		return
	}
	d := decodePlaybook(target, data)
	if !d.ok {
		w.useFile(target, FileUnreadable)
		w.addNotes(d.notes...)
		return
	}
	w.visit(target, d, depth)
}
