package notifytest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Golden compares got with the file at path, or rewrites the file when
// update is set (each test package's own -update flag). A JSON body is
// stored indented, so a reviewer reads the golden file, not one long line.
func Golden(t testing.TB, path string, got []byte, update bool) {
	t.Helper()
	var pretty bytes.Buffer
	if json.Indent(&pretty, got, "", "  ") == nil {
		pretty.WriteByte('\n')
		got = pretty.Bytes()
	}
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs (run with -update to accept):\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}
