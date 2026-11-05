package varsource

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	"gopkg.in/yaml.v3"
)

var (
	errTooLarge   = errors.New("file too large")
	errNotRegular = errors.New("not a regular file")
	// errGivenUp: the walk passed one of its limits and reads nothing more.
	errGivenUp = errors.New("walk given up")
)

// readLimited reads a regular file of at most MaxFileBytes. A larger file
// returns its first MaxFileBytes+1 bytes with errTooLarge.
func readLimited(fsys fs.FS, name string) ([]byte, error) {
	return readUpTo(fsys, name, MaxFileBytes)
}

// readUpTo checks that name is a regular file before it opens it: opening
// a FIFO or a device (one a symlink leads to, say) can block forever.
func readUpTo(fsys fs.FS, name string, limit int) ([]byte, error) {
	if info, err := fs.Stat(fsys, name); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return data, errTooLarge
	}
	return data, nil
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// stateOf is the file state of a read error.
func stateOf(err error) FileState {
	if isNotExist(err) {
		return FileMissing
	}
	return FileUnreadable
}

// readNote is the warning for a file that could not be read. It names no
// error text: that may carry paths of the machine reading the repo.
func readNote(file string, err error) Note {
	switch {
	case errors.Is(err, errTooLarge):
		return warning(codeFileTooLarge, file, 0, "larger than %d KiB; not read", MaxFileBytes>>10)
	case isNotExist(err):
		return warning(codeReadFailed, file, 0, "not found")
	case errors.Is(err, errNotRegular):
		return warning(codeReadFailed, file, 0, "not a regular file; not read")
	case errors.Is(err, fs.ErrPermission):
		return warning(codeReadFailed, file, 0, "permission denied; not read")
	}
	return warning(codeReadFailed, file, 0, "could not be read")
}

// read reads a file for the walk, counting its bytes against the walk's
// limit. errGivenUp once the walk is over.
func (w *walk) read(name string) ([]byte, error) {
	if w.over != "" {
		return nil, errGivenUp
	}
	data, err := readLimited(w.p.fsys, name)
	w.rawBytes += len(data)
	if w.rawBytes > maxWalkBytes {
		w.giveUp(fmt.Sprintf("its imports and roles add up to more than %d MiB", maxWalkBytes>>20))
		return nil, errGivenUp
	}
	return data, err
}

// decoded is a playbook file parsed and checked to be a list of plays. ok
// is false otherwise, with the one note why; notPlaybook tells a file that
// parsed but is no list of plays.
type decoded struct {
	y           *yamlFile
	doc         *yaml.Node
	ok          bool
	notPlaybook bool
	notes       []Note
}

func decodePlaybook(file string, data []byte) decoded {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return decoded{notes: []Note{invalidYAML(file, err)}}
	}
	y := newYAMLFile(file)
	if isPlaybook(y, documentRoot(&doc)) {
		return decoded{y: y, doc: &doc, ok: true}
	}
	if n, over := y.tooComplex(); over {
		return decoded{notes: []Note{n}}
	}
	return decoded{notPlaybook: true, notes: []Note{warning(codeNotAPlaybook, file, 0,
		"not a list of plays (each a mapping with hosts or import_playbook); nothing read from this file")}}
}

// isPlaybook reports whether root is a non-empty list of mappings, each
// with hosts or import_playbook.
func isPlaybook(y *yamlFile, root *yaml.Node) bool {
	if root == nil || root.Kind != yaml.SequenceNode || len(root.Content) == 0 {
		return false
	}
	for _, it := range y.items(root) {
		if p := resolve(it); p == nil || p.Kind != yaml.MappingNode {
			return false
		}
		a := y.attrs(it)
		_, hosts := a["hosts"]
		_, imports := importTarget(a)
		if !hosts && !imports {
			return false
		}
	}
	return !y.exceeded()
}
