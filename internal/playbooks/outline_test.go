package playbooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func entryOf(section, kind, name string, line, depth int, check bool, tags ...string) OutlineEntry {
	if tags == nil {
		tags = []string{}
	}
	return OutlineEntry{Section: section, Kind: kind, Name: name, Line: line, Depth: depth, Tags: tags, RunsInCheckMode: check}
}

func TestOutlineEntries(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []OutlineEntry
	}{
		{
			name: "named and unnamed task use the module key",
			yaml: "- hosts: all\n  tasks:\n    - name: install\n      apt: {name: nginx}\n    - ansible.builtin.service: {name: nginx}\n    - with_items: [a]\n      debug: {msg: x}\n",
			want: []OutlineEntry{
				entryOf("tasks", "task", "install", 3, 0, true),
				entryOf("tasks", "task", "ansible.builtin.service", 5, 0, true),
				entryOf("tasks", "task", "debug", 6, 0, true),
			},
		},
		{
			name: "tags as string and list, inherited from play and block, sorted unique",
			yaml: "- hosts: all\n  tags: play\n  tasks:\n    - name: a\n      tags: z, b\n      ping:\n    - name: grp\n      tags: [blk, play]\n      block:\n        - name: inner\n          tags: [in]\n          ping:\n",
			want: []OutlineEntry{
				entryOf("tasks", "task", "a", 4, 0, true, "b", "play", "z"),
				entryOf("tasks", "block", "grp", 7, 0, true, "blk", "play"),
				entryOf("tasks", "task", "inner", 10, 1, true, "blk", "in", "play"),
			},
		},
		{
			name: "check_mode false on a task only affects that task",
			yaml: "- hosts: all\n  tasks:\n    - name: a\n      check_mode: false\n      ping:\n    - name: b\n      ping:\n",
			want: []OutlineEntry{
				entryOf("tasks", "task", "a", 3, 0, false),
				entryOf("tasks", "task", "b", 6, 0, true),
			},
		},
		{
			name: "check_mode false on a block reaches descendants",
			yaml: "- hosts: all\n  tasks:\n    - block:\n        - ping:\n        - block:\n            - ping:\n      check_mode: false\n    - ping:\n",
			want: []OutlineEntry{
				entryOf("tasks", "block", "block", 3, 0, false),
				entryOf("tasks", "task", "ping", 4, 1, false),
				entryOf("tasks", "block", "block", 5, 1, false),
				entryOf("tasks", "task", "ping", 6, 2, false),
				entryOf("tasks", "task", "ping", 8, 0, true),
			},
		},
		{
			name: "check_mode false on the play reaches every section",
			yaml: "- hosts: all\n  check_mode: no\n  pre_tasks:\n    - ping:\n  roles: [web]\n  handlers:\n    - name: h\n      ping:\n",
			want: []OutlineEntry{
				entryOf("pre_tasks", "task", "ping", 4, 0, false),
				entryOf("roles", "role", "web", 5, 0, false),
				entryOf("handlers", "task", "h", 7, 0, false),
			},
		},
		{
			name: "block nested in rescue and always gains depth",
			yaml: "- hosts: all\n  tasks:\n    - block:\n        - ping:\n      rescue:\n        - block:\n            - name: deep\n              ping:\n      always:\n        - name: fin\n          ping:\n",
			want: []OutlineEntry{
				entryOf("tasks", "block", "block", 3, 0, true),
				entryOf("tasks", "task", "ping", 4, 1, true),
				entryOf("tasks", "block", "block", 6, 1, true),
				entryOf("tasks", "task", "deep", 7, 2, true),
				entryOf("tasks", "task", "fin", 10, 1, true),
			},
		},
		{
			name: "includes, imports and roles are entries and not followed",
			yaml: "- hosts: all\n  roles:\n    - web\n    - {role: db, tags: [data]}\n    - {name: cache}\n  tasks:\n    - include_tasks: more.yml\n    - name: pick\n      import_tasks: {file: other.yml}\n    - ansible.builtin.include_role: {name: base}\n    - import_role: {name: tls}\n",
			want: []OutlineEntry{
				entryOf("roles", "role", "web", 3, 0, true),
				entryOf("roles", "role", "db", 4, 0, true, "data"),
				entryOf("roles", "role", "cache", 5, 0, true),
				entryOf("tasks", "include", "more.yml", 7, 0, true),
				entryOf("tasks", "import", "pick", 8, 0, true),
				entryOf("tasks", "include", "base", 10, 0, true),
				entryOf("tasks", "import", "tls", 11, 0, true),
			},
		},
		{
			name: "a play with only roles",
			yaml: "- hosts: web\n  roles:\n    - nginx\n",
			want: []OutlineEntry{entryOf("roles", "role", "nginx", 3, 0, true)},
		},
		{
			name: "sections in run order, odd items skipped",
			yaml: "- hosts: all\n  handlers:\n    - name: h\n      ping:\n  post_tasks:\n    - name: p\n      ping:\n  tasks:\n    - just a string\n    - name: t\n      ping:\n  pre_tasks:\n    - name: pre\n      ping:\n",
			want: []OutlineEntry{
				entryOf("pre_tasks", "task", "pre", 13, 0, true),
				entryOf("tasks", "task", "t", 10, 0, true),
				entryOf("post_tasks", "task", "p", 6, 0, true),
				entryOf("handlers", "task", "h", 3, 0, true),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := Outline([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if len(o.Plays) != 1 {
				t.Fatalf("plays = %d", len(o.Plays))
			}
			if !reflect.DeepEqual(o.Plays[0].Entries, tc.want) {
				t.Fatalf("entries\n got %+v\nwant %+v", o.Plays[0].Entries, tc.want)
			}
		})
	}
}

func TestOutlinePlaysTagsAndCounts(t *testing.T) {
	o, err := Outline([]byte(`- name: web
  hosts: "a,b"
  become: true
  tags: [site]
  roles: [r1]
  tasks:
    - name: t1
      tags: [always, x]
      ping:
    - block:
        - ping:
    - include_tasks: f.yml
  handlers:
    - name: h1
      ping:
    - import_tasks: g.yml
- hosts: db
  tasks:
    - name: t2
      tags: x
      ping:
- import_playbook: other.yml
`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Truncated {
		t.Fatal("truncated")
	}
	if got := len(o.Plays); got != 3 {
		t.Fatalf("plays = %d", got)
	}
	p := o.Plays[0]
	if p.Name != "web" || p.Hosts != "a,b" || !p.Become || p.Line != 1 || !reflect.DeepEqual(p.Tags, []string{"site"}) {
		t.Fatalf("play 1 = %+v", p)
	}
	if p := o.Plays[1]; p.Name != "db" || p.Hosts != "db" || p.Become || p.Line != 17 {
		t.Fatalf("play 2 = %+v", p)
	}
	if p := o.Plays[2]; p.Name != "other.yml" || p.Line != 22 || len(p.Entries) != 0 || p.Entries == nil {
		t.Fatalf("import_playbook = %+v", p)
	}
	if want := (OutlineCounts{Plays: 3, Tasks: 4, Handlers: 2}); o.Counts != want {
		t.Fatalf("counts = %+v, want %+v", o.Counts, want)
	}
	if want := []string{"always", "site", "x"}; !reflect.DeepEqual(o.Tags, want) {
		t.Fatalf("tags = %v, want %v", o.Tags, want)
	}
}

func TestOutlineNoTagsIsEmptyListNotNull(t *testing.T) {
	o, err := Outline([]byte("- hosts: all\n"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Tags == nil || o.Plays[0].Tags == nil || o.Plays[0].Entries == nil {
		t.Fatalf("nil slices in %+v", o)
	}
}

func TestOutlineCapTruncates(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  tasks:\n")
	for i := range maxOutlineEntries + 5 {
		fmt.Fprintf(&b, "    - name: t%d\n      ping:\n", i)
	}
	b.WriteString("- hosts: more\n  tasks:\n    - ping:\n")
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated {
		t.Fatal("not truncated")
	}
	total := 0
	for _, p := range o.Plays {
		total += len(p.Entries)
	}
	if got := total + len(o.Plays); got != maxOutlineEntries {
		t.Fatalf("plays+entries = %d, want %d", got, maxOutlineEntries)
	}
}

func TestOutlineExactlyAtCapIsNotTruncated(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  tasks:\n")
	for range maxOutlineEntries - 1 { // plus the play itself
		b.WriteString("    - ping:\n")
	}
	o, err := Outline([]byte(b.String()))
	if err != nil || o.Truncated || len(o.Plays[0].Entries) != maxOutlineEntries-1 {
		t.Fatalf("err=%v truncated=%v", err, o.Truncated)
	}
}

func TestOutlineDepthLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  tasks:\n")
	for i := range maxOutlineDepth + 5 {
		b.WriteString(strings.Repeat("  ", i+2) + "- block:\n")
	}
	b.WriteString(strings.Repeat("  ", maxOutlineDepth+7) + "- ping:\n")
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	entries := o.Plays[0].Entries
	if last := entries[len(entries)-1]; last.Depth != maxOutlineDepth || !o.Truncated {
		t.Fatalf("deepest depth %d, truncated %v", last.Depth, o.Truncated)
	}
}

func TestOutlineAliasLoopIsBounded(t *testing.T) {
	o, err := Outline([]byte("- hosts: all\n  tasks:\n    - &b\n      block:\n        - *b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Plays[0].Entries) == 0 || len(o.Plays[0].Entries) > maxOutlineDepth+2 {
		t.Fatalf("entries = %d", len(o.Plays[0].Entries))
	}
}

func TestOutlineInvalid(t *testing.T) {
	for _, in := range []string{"", "hosts: all\n", "- foo: bar\n", "- hosts: [\n", "- name: x\n"} {
		if _, err := Outline([]byte(in)); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q: err = %v", in, err)
		}
	}
	if _, err := Outline([]byte(strings.Repeat("a", MaxInlineBytes+1))); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestOutlineToleratesOddLaterPlays(t *testing.T) {
	o, err := Outline([]byte("- hosts: all\n- just text\n- hosts: [db, cache]\n  tasks: nope\n  roles:\n    - 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Plays) != 2 || o.Plays[1].Hosts != "db,cache" {
		t.Fatalf("plays = %+v", o.Plays)
	}
}

// size is what a result costs the caller: its plays plus all their entries.
func size(o PlaybookOutline) int {
	n := len(o.Plays)
	for _, p := range o.Plays {
		n += len(p.Entries)
	}
	return n
}

func TestOutlineManyPlaysAreBounded(t *testing.T) {
	o, err := Outline([]byte(strings.Repeat("- hosts: a\n", 5000)))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated || len(o.Plays) != maxOutlineEntries {
		t.Fatalf("plays = %d, truncated = %v", len(o.Plays), o.Truncated)
	}
}

// One anchored list shared by every section of every play: the amplification
// that made a 512 KiB document cost a minute and megabytes of output.
func TestOutlineAliasedLargeListIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: a\n  x: &s [" + strings.Repeat("1,", 100000) + "1]\n")
	b.WriteString("- &p {hosts: a, pre_tasks: *s, tasks: *s, post_tasks: *s, handlers: *s}\n")
	for range 20000 {
		b.WriteString("- *p\n")
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated || size(o) > maxOutlineEntries {
		t.Fatalf("truncated = %v, size = %d", o.Truncated, size(o))
	}
}

func TestOutlineAliasedBlockFanOutIsBounded(t *testing.T) {
	o, err := Outline([]byte("- hosts: a\n  tasks:\n    - &b {block: [1,1,1,1,1,1,1,1,1,1], rescue: [1,1,1,1,1,1,1,1,1,1], always: [1,1,1,1,1,1,1,1,1,1]}\n    - &c {block: [*b,*b,*b,*b,*b], rescue: [*b,*b,*b], always: [*b,*b]}\n    - {block: [*c,*c,*c,*c,*c,*c,*c,*c]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if size(o) > maxOutlineEntries {
		t.Fatalf("size = %d", size(o))
	}
}

func checkTagBounds(t *testing.T, o PlaybookOutline, wantEntries int) {
	t.Helper()
	if !o.Truncated || len(o.Tags) > maxOutlineTags {
		t.Fatalf("truncated = %v, union = %d", o.Truncated, len(o.Tags))
	}
	n := 0
	for _, p := range o.Plays {
		if len(p.Tags) > maxOutlineTagsPerList {
			t.Fatalf("play has %d tags", len(p.Tags))
		}
		for _, e := range p.Entries {
			n++
			if len(e.Tags) > maxOutlineTagsPerList {
				t.Fatalf("entry %q has %d tags", e.Name, len(e.Tags))
			}
		}
	}
	if n != wantEntries {
		t.Fatalf("entries = %d, want %d", n, wantEntries)
	}
}

func TestOutlineHugePlayTagListIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: a\n  tags: [")
	for i := range 50000 {
		fmt.Fprintf(&b, "t%d,", i)
	}
	b.WriteString("x]\n  tasks:\n")
	for range 1999 {
		b.WriteString("    - ping:\n")
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	checkTagBounds(t, o, 1999)
}

func TestOutlineAliasedTagListIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: a\n  vars: {l: &l [" + strings.Repeat("1,", 100000) + "1]}\n  tasks:\n")
	for i := range 2000 {
		fmt.Fprintf(&b, "    - {name: n%d, tags: *l, ping: }\n", i)
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	checkTagBounds(t, o, 1999)
}

func TestOutlineDistinctTagsAcrossEntriesAreBoundedInUnion(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: a\n  tasks:\n")
	for i := range 100 {
		b.WriteString("    - ping:\n      tags: [")
		for j := range 20 {
			fmt.Fprintf(&b, "t%d_%d,", i, j)
		}
		b.WriteString("z]\n")
	}
	o, err := Outline([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !o.Truncated || len(o.Tags) != maxOutlineTags {
		t.Fatalf("truncated = %v, union = %d", o.Truncated, len(o.Tags))
	}
}

func TestOutlineHugeTextIsBounded(t *testing.T) {
	big := strings.Repeat("a", 400_000)
	for name, task := range map[string]string{
		"name": "    - {name: *big, ping: }\n",
		"tag":  "    - {tags: *big, ping: }\n",
	} {
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("- hosts: a\n  vars: {big: &big \"" + big + "\"}\n  tasks:\n")
			for range 2000 {
				b.WriteString(task)
			}
			o, err := Outline([]byte(b.String()))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			if !o.Truncated || len(raw) > 2<<20 {
				t.Fatalf("truncated = %v, json = %d bytes", o.Truncated, len(raw))
			}
		})
	}
}

func TestOutlineHugeHostsAndPlayNameAreClipped(t *testing.T) {
	big := strings.Repeat("h", 150_000)
	o, err := Outline([]byte("- name: " + big + "\n  hosts: " + big + "\n- import_playbook: " + big + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range o.Plays {
		if len(p.Name) > maxOutlineText || len(p.Hosts) > maxOutlineText {
			t.Fatalf("play not clipped: %d, %d bytes", len(p.Name), len(p.Hosts))
		}
	}
	if !o.Truncated {
		t.Fatal("not truncated")
	}
}

func TestOutlineClipKeepsValidUTF8(t *testing.T) {
	for _, r := range []string{"é", "日", "😀"} {
		name := strings.Repeat(r, 400)
		o, err := Outline([]byte("- hosts: a\n  tasks:\n    - name: " + name + "\n      ping:\n"))
		if err != nil {
			t.Fatal(err)
		}
		got := o.Plays[0].Entries[0].Name
		if !utf8.ValidString(got) || len(got) > maxOutlineText || !strings.HasSuffix(got, "…") || !o.Truncated {
			t.Fatalf("%q: %q (%d bytes), truncated %v", r, got, len(got), o.Truncated)
		}
	}
}

func TestOutlineShortTextIsUntouched(t *testing.T) {
	o, err := Outline([]byte("- hosts: a\n  tasks:\n    - name: " + strings.Repeat("n", maxOutlineText) + "\n      tags: [" + strings.Repeat("g", maxOutlineText) + "]\n      ping:\n"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Truncated || len(o.Tags) != 1 {
		t.Fatalf("truncated = %v, tags = %d", o.Truncated, len(o.Tags))
	}
}
