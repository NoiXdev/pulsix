package runner

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// playUIDHelper is a stand-in setuid helper that keeps the play uid's one
// rule a unit test can model without a second uid: it may unlink only in
// a directory its group can write. Its rm refuses (exit 1, rm's own words)
// every path whose parent is not group-writable, and removes the rest; any
// other program is exec'd. Each call is recorded for recordedCalls.
func playUIDHelper(t *testing.T) (helper, calls string) {
	t.Helper()
	dir := t.TempDir()
	helper, calls = filepath.Join(dir, "play-uid-helper"), filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" >> " + shQuote(calls) + "\n" +
		"printf '===\\n' >> " + shQuote(calls) + "\n" +
		"shift\n" +
		"prog=$1\nshift\n" +
		"case \"$prog\" in\n" +
		"*/rm)\n" +
		"  shift 2\n" +
		"  st=0\n" +
		"  for p in \"$@\"; do\n" +
		"    if [ \"$(ls -ld \"$(dirname \"$p\")\" | cut -c6)\" != w ]; then\n" +
		"      echo \"rm: cannot remove '$p': Permission denied\" >&2; st=1; continue\n" +
		"    fi\n" +
		"    \"$prog\" -rf -- \"$p\" || st=1\n" +
		"  done\n" +
		"  exit $st;;\n" +
		"*) exec \"$prog\" \"$@\";;\n" +
		"esac\n"
	writeMode(t, helper, script, 0o700)
	return helper, calls
}

// stuckEntry is a temp entry as an install under the play uid leaves it
// (prepareInstall's modes) with a read-only directory in roles/ — a role
// tarball's own mode — that the worker cannot empty.
func stuckEntry(t *testing.T) (entry, stuck string) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root ignores the permission bits this case is built from")
	}
	in := galaxyInstall(t, requirements.KindRole)
	mkdirMode(t, filepath.Join(in.Entry, "home", ".ssh"), 0o700)
	if err := prepareInstall(in); err != nil {
		t.Fatal(err)
	}
	stuck = filepath.Join(in.Entry, "roles", "r")
	mkdirMode(t, stuck, 0o700)
	writeMode(t, filepath.Join(stuck, "file"), "x", 0o600)
	mkdirMode(t, stuck, 0o500)
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	return in.Entry, stuck
}

// TestGalaxyRemoveAllAsThePlayUID: the play uid clears what it wrote, the
// contents of the four directories it could write to, and only those; the
// worker removes the rest. A clean pass logs nothing.
func TestGalaxyRemoveAllAsThePlayUID(t *testing.T) {
	entry, stuck := stuckEntry(t)
	helper, calls := playUIDHelper(t)
	var logs bytes.Buffer
	if err := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: helper}, &logs).RemoveAll(entry); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Lstat(entry); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s survived: %v", entry, err)
	}
	if logs.Len() != 0 {
		t.Errorf("a removal that succeeded logged:\n%s", logs.String())
	}
	got := recordedCalls(t, calls)
	if len(got) != 2 {
		t.Fatalf("helper calls = %q, want chmod then rm", got)
	}
	chmod, rm := got[0], got[1]
	if len(chmod) != 6 || chmod[0] != "65533" || !filepath.IsAbs(chmod[1]) || filepath.Base(chmod[1]) != "chmod" ||
		!slices.Equal(chmod[2:], []string{"-R", "u+rwX", "--", stuck}) {
		t.Errorf("first call = %q, want 65533 <abs chmod> -R u+rwX -- %s", chmod, stuck)
	}
	if len(rm) != 5 || rm[0] != "65533" || !filepath.IsAbs(rm[1]) || filepath.Base(rm[1]) != "rm" ||
		!slices.Equal(rm[2:], []string{"-rf", "--", stuck}) {
		t.Errorf("second call = %q, want 65533 <abs rm> -rf -- %s", rm, stuck)
	}
}

// TestGalaxyRemoveAllReportsWhatIsLeft: when the play uid's pass fails and
// the worker still cannot remove the entry, the error names both.
func TestGalaxyRemoveAllReportsWhatIsLeft(t *testing.T) {
	entry, _ := stuckEntry(t)
	helper := filepath.Join(t.TempDir(), "refusing-helper")
	writeMode(t, helper, "#!/bin/sh\necho 'pulsix-setuid-exec: refused' >&2\nexit 1\n", 0o700)
	var logs bytes.Buffer
	err := testGalaxy(worker.Config{PlayUID: 65533, SetuidHelper: helper}, &logs).RemoveAll(entry)
	if err == nil || !strings.Contains(err.Error(), "pulsix-setuid-exec: refused") || !strings.Contains(err.Error(), "roles") {
		t.Errorf("RemoveAll = %v, want the leftover and the helper's refusal", err)
	}
}

// TestGalaxyRemoveAllWithoutAPlayUID: nothing runs as another uid; the
// worker's own error is the answer.
func TestGalaxyRemoveAllWithoutAPlayUID(t *testing.T) {
	entry, _ := stuckEntry(t)
	helper, calls := playUIDHelper(t)
	var logs bytes.Buffer
	err := testGalaxy(worker.Config{SetuidHelper: helper}, &logs).RemoveAll(entry)
	if err == nil || !strings.Contains(err.Error(), "roles") {
		t.Errorf("RemoveAll without a play uid = %v, want the error naming the leftover", err)
	}
	if c := recordedCalls(t, calls); len(c) != 0 {
		t.Errorf("the helper ran without a play uid: %q", c)
	}
}
