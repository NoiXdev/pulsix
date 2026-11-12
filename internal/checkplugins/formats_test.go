package checkplugins_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// formatVector is one row of testdata/formats.json, which the SPA's
// TypeScript formats are tested against too: both sides must agree on
// every row.
type formatVector struct {
	Format checkplugins.Format `json:"format"`
	Input  string              `json:"input"`
	Valid  bool                `json:"valid"`
}

func readFormatVectors(t *testing.T) []formatVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/formats.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []formatVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("testdata/formats.json: %v", err)
	}
	return vectors
}

func TestFormatVectors(t *testing.T) {
	for i, v := range readFormatVectors(t) {
		t.Run(fmt.Sprintf("%d %s %q", i, v.Format, v.Input), func(t *testing.T) {
			if got := v.Format.Valid(v.Input); got != v.Valid {
				t.Fatalf("%s.Valid(%q) = %v, want %v", v.Format, v.Input, got, v.Valid)
			}
		})
	}
}

// TestFormatVectorsCoverEveryFormat: every format a descriptor uses has at
// least one accepted and one refused vector, and no vector names a format
// no descriptor uses (a stale row would test nothing the SPA renders).
func TestFormatVectorsCoverEveryFormat(t *testing.T) {
	used := map[checkplugins.Format]bool{}
	for _, d := range checkplugins.All() {
		for _, p := range d.Params {
			if p.Format != "" {
				used[p.Format] = true
			}
		}
	}
	type seen struct{ valid, invalid bool }
	covered := map[checkplugins.Format]*seen{}
	for _, v := range readFormatVectors(t) {
		if !used[v.Format] {
			t.Errorf("vector %q names format %q, which no descriptor uses", v.Input, v.Format)
			continue
		}
		if covered[v.Format] == nil {
			covered[v.Format] = &seen{}
		}
		if v.Valid {
			covered[v.Format].valid = true
		} else {
			covered[v.Format].invalid = true
		}
	}
	for f := range used {
		if c := covered[f]; c == nil || !c.valid || !c.invalid {
			t.Errorf("format %q needs at least one valid and one invalid vector, got %+v", f, c)
		}
	}
}

func TestUnknownFormatAcceptsNothing(t *testing.T) {
	if checkplugins.Format("nope").Valid("") || checkplugins.Format("nope").Valid("x") {
		t.Fatal("an unknown format accepted a value")
	}
}
