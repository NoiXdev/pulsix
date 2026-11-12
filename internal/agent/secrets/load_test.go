package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const looseMarker = "# loose\n"

// markerCheck stands in for the agent's file rule (agent.CheckConfigFile):
// a file whose content starts with looseMarker counts as readable by other
// users, so the tests behave the same on every platform. after runs once
// the file has passed, to change the file behind the check's back.
type markerCheck struct {
	calls int
	after func()
}

func (m *markerCheck) check(path string) error {
	m.calls++
	b, err := os.ReadFile(path) // #nosec G304 -- test file
	if err != nil {
		return err
	}
	if strings.HasPrefix(string(b), looseMarker) {
		return errors.New(path + " is accessible to mode 0644")
	}
	if m.after != nil {
		after := m.after
		m.after = nil
		after()
	}
	return nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	writeFile(t, path, "secrets:\n  pg:\n    password: x\n")
	m := &markerCheck{}
	set, err := Load(path, m.check)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if f, ok := set.Fields("pg"); !ok || f["password"] != "x" {
		t.Fatalf("Fields = %v, %v", f, ok)
	}
	if m.calls != 1 {
		t.Fatalf("check ran %d times, want 1", m.calls)
	}
}

func TestLoadAbsentFile(t *testing.T) {
	m := &markerCheck{}
	set, err := Load(filepath.Join(t.TempDir(), "agent.yaml"), m.check)
	if !errors.Is(err, ErrAbsent) || set.Len() != 0 {
		t.Fatalf("Load = %v, %v; want ErrAbsent and no secrets", set, err)
	}
	if m.calls != 0 {
		t.Fatal("an absent file was checked")
	}
}

func TestLoadRefusesAFileTheCheckRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	// Valid YAML after the marker: had the file been read, it would load.
	writeFile(t, path, looseMarker+"secrets:\n  pg:\n    password: "+secretValue+"\n")
	m := &markerCheck{}
	set, err := Load(path, m.check)
	if err == nil || !strings.Contains(err.Error(), "mode 0644") || set.Len() != 0 {
		t.Fatalf("Load = %v, %v; want the check's refusal and no secrets", set, err)
	}
}

func TestLoadRefusesANonRegularFile(t *testing.T) {
	dir := t.TempDir()
	set, err := Load(dir, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "not a regular file") || set.Len() != 0 {
		t.Fatalf("Load(dir) = %v, %v", set, err)
	}
}

func TestLoadRefusesALargeFileBeforeParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	// Broken YAML quoting a value: a parse would report a syntax error.
	writeFile(t, path, "secrets: ["+secretValue+"\n"+strings.Repeat("#", maxFileBytes))
	_, err := Load(path, func(string) error { return nil })
	if err == nil || err.Error() != path+" is larger than 1 MiB" {
		t.Fatalf("Load = %v", err)
	}
}

func TestLoadParseErrorNamesThePathAndNoValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	writeFile(t, path, "secrets:\n  pg:\n    password: "+secretValue+": x\n")
	_, err := Load(path, func(string) error { return nil })
	want := path + ": line 3: not valid YAML (mapping values are not allowed in this context)"
	if err == nil || err.Error() != want {
		t.Fatalf("Load = %v, want %q", err, want)
	}
}

// TestLoadRechecksAFileReplacedAfterTheCheck is Review Focus 2: an editor
// saves by writing a temporary file and renaming it over agent.yaml, and
// that temporary file can be readable by everyone for a moment. A rename
// landing right after the check passed must not hand the unchecked file's
// content (nor the replaced file's) to the agent: Load notices the swap
// and checks again.
func TestLoadRechecksAFileReplacedAfterTheCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	writeFile(t, path, "secrets:\n  old:\n    a: b\n")
	tmp := filepath.Join(dir, ".agent.yaml.swp")
	writeFile(t, tmp, looseMarker+"secrets:\n  new:\n    password: "+secretValue+"\n")
	var renameErr error
	m := &markerCheck{after: func() { renameErr = os.Rename(tmp, path) }}
	set, err := Load(path, m.check)
	if renameErr != nil {
		t.Skipf("cannot rename over an open file here: %v", renameErr)
	}
	if err == nil || !strings.Contains(err.Error(), "mode 0644") || set.Len() != 0 {
		t.Fatalf("Load = %v, %v; want the replacement refused and no secrets", set, err)
	}
	if m.calls != 2 {
		t.Fatalf("check ran %d times, want 2 (once per file)", m.calls)
	}
}

// TestLoadReadsAFileReplacedByAGoodOne: the same swap with a protected
// replacement simply loads the new file.
func TestLoadReadsAFileReplacedByAGoodOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	writeFile(t, path, "secrets:\n  old:\n    a: b\n")
	tmp := filepath.Join(dir, ".agent.yaml.swp")
	writeFile(t, tmp, "secrets:\n  new:\n    a: b\n")
	var renameErr error
	m := &markerCheck{after: func() { renameErr = os.Rename(tmp, path) }}
	set, err := Load(path, m.check)
	if renameErr != nil {
		t.Skipf("cannot rename over an open file here: %v", renameErr)
	}
	if err != nil || strings.Join(set.Names(), ",") != "new" {
		t.Fatalf("Load = %v, %v; want the new file", set, err)
	}
}

// TestLoadGivesUpOnAFileThatKeepsChanging: a file replaced on every
// attempt is reported, not read forever.
func TestLoadGivesUpOnAFileThatKeepsChanging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	writeFile(t, path, "secrets: {}\n")
	n := 0
	var renameErr error
	check := func(string) error {
		n++
		tmp := filepath.Join(dir, "next")
		writeFile(t, tmp, "secrets: {}\n")
		if err := os.Rename(tmp, path); err != nil {
			renameErr = err
		}
		return nil
	}
	set, err := Load(path, check)
	if renameErr != nil {
		t.Skipf("cannot rename over an open file here: %v", renameErr)
	}
	if err == nil || err.Error() != path+" was replaced while it was read; it is read again at the next check" || set.Len() != 0 {
		t.Fatalf("Load = %v, %v", set, err)
	}
	if n != maxAttempts {
		t.Fatalf("check ran %d times, want %d", n, maxAttempts)
	}
}
