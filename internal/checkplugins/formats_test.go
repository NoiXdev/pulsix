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

// TestFormatVectorsCoverEveryFormat: every format a validator knows has at
// least one accepted and one refused vector — the SPA's parity test asserts
// a TypeScript twin for every format the file names, so this is what makes
// a new format need one — and no vector names a format Formats does not
// list (a stale row would test nothing).
func TestFormatVectorsCoverEveryFormat(t *testing.T) {
	known := map[checkplugins.Format]bool{}
	for _, f := range checkplugins.Formats() {
		known[f] = true
	}
	type seen struct{ valid, invalid bool }
	covered := map[checkplugins.Format]*seen{}
	for _, v := range readFormatVectors(t) {
		if !known[v.Format] {
			t.Errorf("vector %q names format %q, which Formats does not list", v.Input, v.Format)
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
	for f := range known {
		if c := covered[f]; c == nil || !c.valid || !c.invalid {
			t.Errorf("format %q needs at least one valid and one invalid vector, got %+v", f, c)
		}
	}
}

// TestFormatsAreExactlyTheUsedOnes: Formats lists every format a
// descriptor uses, and only those — a format no parameter names would be a
// validator, a TypeScript twin and an API phrase kept for nothing. The
// shared secret parameter (SecretParam) counts as used: it is the one
// spelling every plugin that takes a secret embeds, so its format is
// needed before the first such plugin exists (backlog 3.5, 3.10).
func TestFormatsAreExactlyTheUsedOnes(t *testing.T) {
	// FormatStatusURL is needed by the four web server checks (backlog 3.11),
	// which land after the format; remove it from this seed once nginx is
	// registered.
	used := map[checkplugins.Format]bool{checkplugins.SecretParam().Format: true, checkplugins.FormatStatusURL: true}
	for _, d := range checkplugins.All() {
		for _, p := range d.Params {
			if p.Format != "" {
				used[p.Format] = true
			}
		}
	}
	listed := map[checkplugins.Format]bool{}
	for _, f := range checkplugins.Formats() {
		if listed[f] {
			t.Errorf("Formats lists %q twice", f)
		}
		listed[f] = true
		if !used[f] {
			t.Errorf("Formats lists %q, which no descriptor uses", f)
		}
	}
	for f := range used {
		if !listed[f] {
			t.Errorf("a descriptor uses format %q, which Formats does not list", f)
		}
	}
}

func TestUnknownFormatAcceptsNothing(t *testing.T) {
	if checkplugins.Format("nope").Valid("") || checkplugins.Format("nope").Valid("x") {
		t.Fatal("an unknown format accepted a value")
	}
}
