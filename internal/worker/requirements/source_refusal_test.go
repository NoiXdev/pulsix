package requirements

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// funcDir is an open directory whose entries come from read.
type funcDir struct {
	name string
	read func(n int) ([]fs.DirEntry, error)
}

func (d *funcDir) Stat() (fs.FileInfo, error)           { return fakeInfo{d.name, fs.ModeDir | 0o755}, nil }
func (d *funcDir) Read([]byte) (int, error)             { return 0, io.EOF }
func (d *funcDir) Close() error                         { return nil }
func (d *funcDir) ReadDir(n int) ([]fs.DirEntry, error) { return d.read(n) }

// fakeInfo is the FileInfo of an entry that has nothing but a name and a mode.
type fakeInfo struct {
	name string
	mode fs.FileMode
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

// permFS is a clone that refuses the lookups below vendor/closed with a
// permission error, the way a confined root does for a directory the worker
// cannot enter.
type permFS struct{ refusingFS }

func closed(name string) bool {
	return name == "vendor/closed" || strings.HasPrefix(name, "vendor/closed/")
}

func (f permFS) Lstat(name string) (fs.FileInfo, error) {
	if closed(name) {
		return nil, &fs.PathError{Op: "lstat", Path: name, Err: fs.ErrPermission}
	}
	return f.MapFS.Lstat(name)
}

func (f permFS) Stat(name string) (fs.FileInfo, error) {
	if closed(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrPermission}
	}
	return f.refusingFS.Stat(name)
}

// TestCheckSourcesNamesWhatItCannotCheck: a source below a directory the
// worker cannot read, or with a glob character, is still refused, but the
// message says what happened and not "outside the repository".
func TestCheckSourcesNamesWhatItCannotCheck(t *testing.T) {
	cases := []struct {
		src  string
		want string
		is   error
	}{
		{"vendor/closed/r.tar.gz", `requirements.yml: the source "vendor/closed/r.tar.gz" cannot be read: permission denied`, errUnreadable},
		{"vendor/*", `requirements.yml: the source "vendor/*" holds a glob character (*, ? or [), which is not supported`, errGlob},
		{"vendor/[ab]", `requirements.yml: the source "vendor/[ab]" holds a glob character (*, ? or [), which is not supported`, errGlob},
		// Refused by the clone's Stat, not for a permission: still outside.
		{"vendor/link/x", `requirements.yml: the source "vendor/link/x" is outside the repository`, errOutside},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			clone := permFS{refusingFS{fstest.MapFS{
				"vendor/r.tar.gz": {Data: []byte("tar")},
				"vendor/link/x":   {Data: []byte("x")},
				"vendor/closed/x": {Data: []byte("x")},
			}}}
			err := checkSources(t.Context(), clone, testCloneDir, "", plan{name: "requirements.yml", sources: []string{tc.src}})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("checkSources = %v, want %q", err, tc.want)
			}
			if !errors.Is(err, tc.is) {
				t.Errorf("checkSources = %v, want errors.Is %v", err, tc.is)
			}
			for _, other := range []error{errOutside, errUnreadable, errGlob} {
				if other != tc.is && errors.Is(err, other) {
					t.Errorf("checkSources = %v, must not match %v", err, other)
				}
			}
		})
	}
}

// hugeDirFS lists big as a generator of total regular files, and records
// every batch size it is asked for.
type hugeDirFS struct {
	refusingFS
	total  int
	asked  *[]int
	handed *int
}

func newHugeDirFS(total int) hugeDirFS {
	return hugeDirFS{
		refusingFS: refusingFS{fstest.MapFS{"big/.keep": {Data: []byte("x")}}},
		total:      total,
		asked:      new([]int),
		handed:     new(int),
	}
}

func (f hugeDirFS) Open(name string) (fs.File, error) {
	if name != "big" {
		return f.MapFS.Open(name)
	}
	return &funcDir{name: name, read: func(n int) ([]fs.DirEntry, error) {
		*f.asked = append(*f.asked, n)
		if *f.handed >= f.total {
			return nil, io.EOF
		}
		var out []fs.DirEntry
		for len(out) < n && *f.handed < f.total {
			name := fmt.Sprintf("f%07d", *f.handed)
			out = append(out, fs.FileInfoToDirEntry(fakeInfo{name, 0o644}))
			*f.handed++
		}
		return out, nil
	}}, nil
}

func TestCheckSourceReadsADirectoryInBatches(t *testing.T) {
	t.Run("a directory over the entry limit fails after a few batches", func(t *testing.T) {
		clone := newHugeDirFS(10 * listChunk)
		lim := treeLimits{entries: 2*listChunk + 1, depth: 64, lookups: maxLookups}
		err := newSourceCheck(t.Context(), clone, lim).source(testCloneDir, "", "big")
		if !errors.Is(err, errTooManyEntries) {
			t.Fatalf("source = %v, want errTooManyEntries", err)
		}
		for _, n := range *clone.asked {
			if n != listChunk {
				t.Errorf("ReadDir(%d), want every batch to ask for %d", n, listChunk)
			}
		}
		if *clone.handed > 3*listChunk {
			t.Errorf("%d entries read, want at most %d", *clone.handed, 3*listChunk)
		}
	})
	t.Run("a directory within the limit is read whole", func(t *testing.T) {
		clone := newHugeDirFS(listChunk + 5)
		if err := newSourceCheck(t.Context(), clone, defaultLimits()).source(testCloneDir, "", "big"); err != nil {
			t.Fatalf("source = %v, want nil", err)
		}
		if *clone.handed != listChunk+5 {
			t.Errorf("%d entries read, want %d", *clone.handed, listChunk+5)
		}
	})
}
