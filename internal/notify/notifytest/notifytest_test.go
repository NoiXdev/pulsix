package notifytest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/notify/notifytest"
)

func TestCasesAreDistinctAndComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range notifytest.Cases() {
		if seen[c.Name] || c.Event.Kind == "" || c.Event.Org.Name == "" {
			t.Fatalf("case %q", c.Name)
		}
		seen[c.Name] = true
	}
	if ev := notifytest.LargeBatch(3, 2); ev.Alerts.Count != 5 || len(ev.Alerts.Items) != 3 {
		t.Fatalf("large batch = %+v", ev.Alerts)
	}
}

func TestGoldenWritesIndentedJSONAndCompares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.golden.json")
	notifytest.Golden(t, path, []byte(`{"a":1}`), true)
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "\n  \"a\": 1\n") {
		t.Fatalf("written %q, %v", b, err)
	}
	notifytest.Golden(t, path, []byte(`{"a":1}`), false)
	text := filepath.Join(t.TempDir(), "plain.golden")
	notifytest.Golden(t, text, []byte("not json"), true)
	notifytest.Golden(t, text, []byte("not json"), false)
}
