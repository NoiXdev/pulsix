package playbooks

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Backlog 3.129: names an outline falls back on, and the bounds on the tags
// it emits.

func TestOutlineNamesFallBackToTheKind(t *testing.T) {
	tests := []struct {
		name, yaml string
		want       []string // entry names of play 1
	}{
		{"keyword-only task", "- hosts: a\n  tasks:\n    - when: x\n    - async: 10\n      poll: 0\n", []string{"task", "task"}},
		{"null role", "- hosts: a\n  roles:\n    - {role: ~}\n    - {tags: [x]}\n", []string{"role", "role"}},
		{"async, poll and friends are never the name", "- hosts: a\n  tasks:\n    - async: 10\n      poll: 0\n      apt: {name: x}\n    - retries: 3\n      delay: 1\n      until: x\n      ping:\n", []string{"apt", "ping"}},
		{"action names the module", "- hosts: a\n  tasks:\n    - action: apt name=nginx\n    - local_action: {module: copy, src: a}\n    - action: ~\n", []string{"apt", "copy", "task"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := Outline([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range o.Plays[0].Entries {
				got = append(got, e.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("names = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOutlineImportPlaybookWithoutPathFallsBackToTheKind(t *testing.T) {
	for _, v := range []string{"[x]", "~", "{a: b}"} {
		o, err := Outline([]byte("- hosts: a\n- import_playbook: " + v + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := o.Plays[1].Name; got != KindImport {
			t.Fatalf("import_playbook: %s: name = %q, want %q", v, got, KindImport)
		}
	}
}

func totalTags(o PlaybookOutline) int {
	n := 0
	for _, p := range o.Plays {
		n += len(p.Tags)
		for _, e := range p.Entries {
			n += len(e.Tags)
		}
	}
	return n
}

func TestOutlineTagsAreBudgetedAcrossTheOutline(t *testing.T) {
	var tags []string
	for i := range maxOutlineTagsPerList {
		tags = append(tags, fmt.Sprintf("%s%d", strings.Repeat("t", 250), i))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- hosts: a\n  tags: [%s]\n  tasks:\n", strings.Join(tags, ","))
	for i := range 1999 {
		fmt.Fprintf(&b, "    - {name: n%d, tags: [own%d], ping: }\n", i, i)
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated {
		t.Fatal("truncated = false, want true once the tag budget is used")
	}
	if n := totalTags(o); n > maxOutlineTagsTotal {
		t.Fatalf("%d tags emitted, budget %d", n, maxOutlineTagsTotal)
	}
	raw, _ := json.Marshal(o)
	if len(raw) > 1<<20+1<<16 {
		t.Fatalf("outline JSON is %d bytes", len(raw))
	}
	// Past the budget an entry still carries its own tags, not the play's.
	mid := o.Plays[0].Entries[1000]
	if len(mid.Tags) != 1 || mid.Tags[0] != "own1000" {
		t.Fatalf("entry 1000 tags = %v, want its own only", mid.Tags)
	}
	// Early entries inherit as before.
	if got := len(o.Plays[0].Entries[0].Tags); got != maxOutlineTagsPerList {
		t.Fatalf("first entry has %d tags, want %d", got, maxOutlineTagsPerList)
	}
}

func TestOutlineAliasedOwnTagsStayUnderTheBudget(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: a\n  vars: {l: &l [" + strings.Repeat("1,", 100000) + "1]}\n  tasks:\n")
	for i := range 1999 {
		fmt.Fprintf(&b, "    - {name: n%d, tags: *l, ping: }\n", i)
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if n := totalTags(o); n > maxOutlineTagsTotal {
		t.Fatalf("%d tags emitted, budget %d", n, maxOutlineTagsTotal)
	}
}

// A comma-separated tags string is cut by length before it is split, so what
// lies beyond the cut is never read.
func TestOutlineTagsStringIsCutBeforeItIsScanned(t *testing.T) {
	text := "early," + strings.Repeat("x", maxOutlineTagsText) + ",late"
	o, err := Outline([]byte("- hosts: a\n  tasks:\n    - {tags: \"" + text + "\", ping: }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated {
		t.Fatal("truncated = false")
	}
	for _, tag := range o.Plays[0].Entries[0].Tags {
		if tag == "late" {
			t.Fatal("a tag beyond the length cut was read")
		}
	}
	if got := o.Plays[0].Entries[0].Tags; len(got) == 0 || got[len(got)-1] != "early" && got[0] != "early" {
		t.Fatalf("tags = %v, want early kept", got)
	}
}

// A huge list item is measured before it is split.
func TestOutlineTagsListItemIsMeasuredBeforeSplit(t *testing.T) {
	huge := strings.Repeat("a,", maxOutlineTagsText) + "late"
	o, err := Outline([]byte("- hosts: a\n  tasks:\n    - {tags: [first, \"" + huge + "\"], ping: }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated {
		t.Fatal("truncated = false")
	}
	for _, tag := range o.Plays[0].Entries[0].Tags {
		if tag == "late" {
			t.Fatal("a tag beyond the length cut was read")
		}
	}
}
