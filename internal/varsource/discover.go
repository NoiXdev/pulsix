package varsource

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// headBytes is how much of a file past the playbook cap is read to tell
// whether it may be a playbook.
const headBytes = 64 << 10

// skippedDirs hold no playbooks of their own (roles, inventory variables,
// installed collections).
var skippedDirs = map[string]bool{"roles": true, "group_vars": true, "host_vars": true, "collections": true}

// ParseRepo discovers the playbooks under fsys (the repo subpath) in
// lexical order and reads each one's variable sources (spec 7.75 section
// 4). It never fails: problems are notes, in the playbooks or in the
// report's own list.
func ParseRepo(fsys fs.FS, commit string) Report {
	d := &discovery{p: &parser{fsys: fsys}}
	if err := fs.WalkDir(fsys, ".", d.visit); err != nil {
		d.notes = append(d.notes, warning(codeReadFailed, "", 0, "the repository could not be read; no playbooks discovered"))
	}
	r := Report{Commit: commit, Playbooks: d.playbooks, Notes: d.notes}
	if r.Playbooks == nil {
		r.Playbooks = []Playbook{}
	}
	if d.leftOut > 0 {
		r.Incomplete = true
		r.LeftOut = d.listed
		r.Notes = append(r.Notes, warning(codeTooManyPlaybooks, "", 0, "more than %d playbooks; %d files that may be playbooks are left out: %s",
			MaxPlaybooks, d.leftOut, nameList(d.listed, d.leftOut)))
	}
	r.Notes = capNotes(r.Notes, "", maxNotesPerList)
	return r
}

type discovery struct {
	p         *parser
	playbooks []Playbook
	notes     []Note
	listed    []string // left out, up to MaxPlaybooks names
	leftOut   int
}

func (d *discovery) visit(name string, entry fs.DirEntry, err error) error {
	if err != nil {
		if name == "." {
			return err
		}
		d.notes = append(d.notes, readNote(name, err))
		if entry != nil && entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if entry.IsDir() {
		if name != "." && skipDir(entry.Name()) {
			return fs.SkipDir
		}
		return nil
	}
	if !candidate(entry.Name()) || (!entry.Type().IsRegular() && entry.Type()&fs.ModeSymlink == 0) {
		return nil
	}
	if len(d.playbooks) == MaxPlaybooks {
		d.maybeLeftOut(name)
		return nil
	}
	d.examine(name)
	return nil
}

func skipDir(name string) bool {
	return skippedDirs[name] || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".galaxy")
}

// candidate is a YAML file name that may hold a playbook: not a galaxy
// requirements file and not a sidecar.
func candidate(name string) bool {
	ext := path.Ext(name)
	if ext != ".yml" && ext != ".yaml" {
		return false
	}
	base := strings.TrimSuffix(name, ext)
	return base != "requirements" && !strings.HasSuffix(base, ".argument_specs")
}

// examine reads one candidate. Only a file that starts like a list is
// parsed, and only problems of such a file are noted: a vars file or a
// manifest is no playbook and says nothing.
func (d *discovery) examine(name string) {
	data, err := readLimited(d.p.fsys, name)
	switch {
	case errors.Is(err, errTooLarge):
		if mayBePlaybook(data) {
			d.notes = append(d.notes, readNote(name, err))
		}
		return
	case errors.Is(err, errNotRegular):
		return
	case err != nil:
		d.notes = append(d.notes, readNote(name, err))
		return
	}
	if !mayBePlaybook(data) {
		return
	}
	dec := decodePlaybook(name, data)
	switch {
	case dec.notPlaybook:
		return
	case !dec.ok:
		d.notes = append(d.notes, dec.notes...)
		return
	case !validPlaybookPath(name):
		d.notes = append(d.notes, warning(codePathUnsupported, name, 0,
			"a playbook path is letters, digits, dots, slashes and hyphens ending in .yml or .yaml, without \"..\"; not read"))
		return
	}
	d.playbooks = append(d.playbooks, d.p.parse(name, dec, len(data)))
}

// maybeLeftOut counts a file past the playbook cap that may be a playbook;
// it is not parsed, only its start is looked at.
func (d *discovery) maybeLeftOut(name string) {
	if !validPlaybookPath(name) {
		return
	}
	head, err := readUpTo(d.p.fsys, name, headBytes)
	if (err != nil && !errors.Is(err, errTooLarge)) || !mayBePlaybook(head) {
		return
	}
	d.leftOut++
	if len(d.listed) < MaxPlaybooks {
		d.listed = append(d.listed, name)
	}
}

// mayBePlaybook reports whether data starts like a YAML list: its first
// content (after comments, directives and a document start) is a block
// sequence entry, a flow sequence, or an anchor or tag that may open one.
func mayBePlaybook(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for len(data) > 0 {
		var line []byte
		line, data, _ = bytes.Cut(data, []byte("\n"))
		t := bytes.Trim(line, " \t\r")
		if after, ok := bytes.CutPrefix(t, []byte("---")); ok && (len(after) == 0 || after[0] == ' ' || after[0] == '\t') {
			t = bytes.TrimLeft(after, " \t")
		}
		if len(t) == 0 || t[0] == '#' || t[0] == '%' || bytes.Equal(t, []byte("...")) {
			continue
		}
		switch t[0] {
		case '[', '&', '!':
			return true
		case '-':
			return len(t) == 1 || t[1] == ' ' || t[1] == '\t'
		}
		return false
	}
	return false
}

// nameList lists names for a message: as many as fit about 300 bytes, then
// how many in all.
func nameList(names []string, total int) string {
	var b strings.Builder
	shown := 0
	for _, n := range names {
		if b.Len() > 300 {
			break
		}
		if shown > 0 {
			b.WriteString(", ")
		}
		b.WriteString(short(n))
		shown++
	}
	if shown < total {
		fmt.Fprintf(&b, " … (%d in all)", total)
	}
	return b.String()
}
