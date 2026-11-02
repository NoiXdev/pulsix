package worker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestLivePIDsOfUIDReadsTheProcessTable pins the probe the sweep is
// checked with (backlog 7.35): a process counts when its real, effective
// or saved uid is the play uid, and a zombie, already dead, does not. A
// process that exits between the listing and the read is not an error.
func TestLivePIDsOfUIDReadsTheProcessTable(t *testing.T) {
	root := t.TempDir()
	write := func(pid, uids, state string) {
		t.Helper()
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		status := "Name:\tsleep\nState:\t" + state + "\nTgid:\t" + pid + "\nUid:\t" + uids + "\nGid:\t65532\t65532\t65532\t65532\n"
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("10", "65533\t65533\t65533\t65533", "S (sleeping)")
	write("11", "1000\t1000\t1000\t1000", "S (sleeping)")
	write("12", "65533\t65533\t65533\t65533", "Z (zombie)")
	write("13", "1000\t65533\t1000\t1000", "R (running)")
	write("14", "1000\t1000\t65533\t1000", "S (sleeping)")
	if err := os.MkdirAll(filepath.Join(root, "15"), 0o755); err != nil { // gone before its status was read
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := livePIDsOfUID(root, 65533)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []int{10, 13, 14}; !slices.Equal(got, want) {
		t.Fatalf("live pids of 65533 = %v, want %v", got, want)
	}
	if _, err := livePIDsOfUID(filepath.Join(root, "missing"), 65533); err == nil {
		t.Fatal("an unreadable process table answered nothing instead of an error")
	}
}
