package requirements

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"syscall"
)

// Files are the counted requirements files of a commit (spec goal 3).
type Files struct {
	Root  string   // the requirements root relative to the clone: the repo subpath or ""
	Paths []string // clone-relative slash paths, in goal 3 order
}

// Empty reports that no file counts: the commit has nothing to install.
func (f Files) Empty() bool { return len(f.Paths) == 0 }

// Find returns the requirements files of the clone. The requirements root is
// subpath when any file counts there, the clone root otherwise. clone is the
// clone's confined root: a path it refuses (a link out of the clone, .git) is
// an error naming the path, never followed and never taken for missing. A
// directory at a counted location is not counted.
func Find(clone fs.StatFS, subpath string) (Files, error) {
	if subpath != "" {
		subpath = path.Clean(subpath)
		if subpath == "." {
			subpath = ""
		}
	}
	if subpath != "" {
		paths, err := findAt(clone, subpath)
		if err != nil {
			return Files{}, err
		}
		if len(paths) > 0 {
			return Files{Root: subpath, Paths: paths}, nil
		}
	}
	paths, err := findAt(clone, "")
	if err != nil {
		return Files{}, err
	}
	return Files{Paths: paths}, nil
}

// findAt returns the counted files below dir ("" for the clone root): the
// locations in the fixed order of spec goal 3, at each the first regular file
// of .yml and .yaml.
func findAt(clone fs.StatFS, dir string) ([]string, error) {
	var paths []string
	for _, loc := range []string{"requirements", "roles/requirements", "collections/requirements"} {
		for _, ext := range []string{".yml", ".yaml"} {
			p := path.Join(dir, loc+ext)
			ok, err := isRegularFile(clone, p)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if ok {
				paths = append(paths, p)
				break
			}
		}
	}
	return paths, nil
}

// isRegularFile reports whether p is a regular file. A path that does not
// exist, or cannot because a component is a file (ENOTDIR), is not one.
func isRegularFile(clone fs.StatFS, p string) (bool, error) {
	fi, err := clone.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return false, nil
	case err != nil:
		return false, err
	}
	return fi.Mode().IsRegular(), nil
}
