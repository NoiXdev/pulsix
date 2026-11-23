package playbooks

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Backlog 3.129: names of entries without a usable one, and the bounds on
// the tags an outline emits.

// An entry without a usable name is sent with an empty name; the client
// shows a word for its kind in the reader's language.
func TestOutlineEntriesWithoutAUsableNameAreUnnamed(t *testing.T) {
	tests := []struct {
		name, yaml string
		want       []string // entry names of play 1
	}{
		{"keyword-only task", "- hosts: a\n  tasks:\n    - when: x\n    - async: 10\n      poll: 0\n", []string{"", ""}},
		{"null role", "- hosts: a\n  roles:\n    - {role: ~}\n    - {tags: [x]}\n    - ~\n", []string{"", "", ""}},
		{"unnamed block", "- hosts: a\n  tasks:\n    - block:\n        - ping:\n", []string{"", "ping"}},
		{"async, poll and friends are never the name", "- hosts: a\n  tasks:\n    - async: 10\n      poll: 0\n      apt: {name: x}\n    - retries: 3\n      delay: 1\n      until: x\n      ping:\n", []string{"apt", "ping"}},
		{"action names the module", "- hosts: a\n  tasks:\n    - action: apt name=nginx\n    - local_action: {module: copy, src: a}\n    - action: ~\n", []string{"apt", "copy", ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := Outline([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, e := range o.Plays[0].Entries {
				got = append(got, e.Name)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("names = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutlineImportPlaybookWithoutPathIsUnnamed(t *testing.T) {
	for _, v := range []string{"[x]", "~", "{a: b}"} {
		o, err := Outline([]byte("- hosts: a\n- import_playbook: " + v + "\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := o.Plays[1].Name; got != "" {
			t.Fatalf("import_playbook: %s: name = %q, want empty", v, got)
		}
	}
}

// tagBytes is what the outline's play and entry tag lists cost against the
// budget.
func tagBytes(o PlaybookOutline) int {
	n := 0
	for _, p := range o.Plays {
		n += tagListBytes(p.Tags)
		for _, e := range p.Entries {
			n += tagListBytes(e.Tags)
		}
	}
	return n
}

func TestOutlineTagsAreBudgetedAcrossTheOutline(t *testing.T) {
	var tags []string
	for i := range maxOutlineTagsPerList {
		tags = append(tags, fmt.Sprintf("%s%d", strings.Repeat("t", 250), i))
	}
	tests := []struct {
		name, section, item string
		own                 bool // whether an entry has a tag of its own
	}{
		{"tasks", "tasks", "    - {name: n%d, tags: [own%d], ping: }\n", true},
		{"scalar roles", "roles", "    - r%d\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			fmt.Fprintf(&b, "- hosts: a\n  tags: [%s]\n  %s:\n", strings.Join(tags, ","), tc.section)
			for i := range 1999 {
				if tc.own {
					fmt.Fprintf(&b, tc.item, i, i)
				} else {
					fmt.Fprintf(&b, tc.item, i)
				}
			}
			o, err := Outline([]byte(b.String()))
			if err != nil {
				t.Fatal(err)
			}
			if !o.Truncated {
				t.Fatal("truncated = false, want true once the tag budget is used")
			}
			if n := tagBytes(o); n > maxOutlineTagBytes {
				t.Fatalf("%d tag bytes emitted, budget %d", n, maxOutlineTagBytes)
			}
			raw, _ := json.Marshal(o)
			if len(raw) > 3<<19 {
				t.Fatalf("outline JSON is %d bytes", len(raw))
			}
			// Past the budget an entry still carries its own tags, not the play's.
			mid := o.Plays[0].Entries[1000]
			want := []string{}
			if tc.own {
				want = []string{"own1000"}
			}
			if !slices.Equal(mid.Tags, want) {
				t.Fatalf("entry 1000 tags = %v, want %v", mid.Tags, want)
			}
			// Early entries inherit as before.
			if got := len(o.Plays[0].Entries[0].Tags); got < maxOutlineTagsPerList {
				t.Fatalf("first entry has %d tags, want the play's %d", got, maxOutlineTagsPerList)
			}
		})
	}
}

// An ordinary long playbook never reaches the budget: every entry keeps the
// play's few short tags and the outline is complete.
func TestOutlineLongPlaybookWithShortTagsIsNotCut(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: a\n  tags: [p1, p2, p3]\n  tasks:\n")
	for i := range 700 {
		fmt.Fprintf(&b, "    - {name: n%d, ping: }\n", i)
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if o.Truncated {
		t.Fatal("truncated = true for an ordinary playbook")
	}
	for i, e := range o.Plays[0].Entries {
		if !slices.Equal(e.Tags, []string{"p1", "p2", "p3"}) {
			t.Fatalf("entry %d tags = %v, want the play's three", i, e.Tags)
		}
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
	if n := tagBytes(o); n > maxOutlineTagBytes {
		t.Fatalf("%d tag bytes emitted, budget %d", n, maxOutlineTagBytes)
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
	if got := o.Plays[0].Entries[0].Tags; !slices.Contains(got, "early") {
		t.Fatalf("tags = %v, want early kept", got)
	}
}

// A huge list item is measured before it is split: its first part is longer
// than the cut, so "late" is read only when the text is scanned past it.
func TestOutlineTagsListItemIsMeasuredBeforeSplit(t *testing.T) {
	huge := strings.Repeat("x", maxOutlineTagsText+10) + ",late"
	o, err := Outline([]byte("- hosts: a\n  tasks:\n    - {tags: [first, \"" + huge + "\"], ping: }\n"))
	if err != nil {
		t.Fatal(err)
	}
	tags := o.Plays[0].Entries[0].Tags
	if !o.Truncated || !slices.Contains(tags, "first") || slices.Contains(tags, "late") {
		t.Fatalf("truncated = %v, tags = %v: want first kept, late (past the cut) not read", o.Truncated, tags)
	}
}
