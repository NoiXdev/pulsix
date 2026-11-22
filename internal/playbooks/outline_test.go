package playbooks

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
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
	if total != maxOutlineEntries {
		t.Fatalf("entries = %d, want %d", total, maxOutlineEntries)
	}
}

func TestOutlineExactlyAtCapIsNotTruncated(t *testing.T) {
	var b strings.Builder
	b.WriteString("- hosts: all\n  tasks:\n")
	for range maxOutlineEntries {
		b.WriteString("    - ping:\n")
	}
	o, err := Outline([]byte(b.String()))
	if err != nil || o.Truncated || len(o.Plays[0].Entries) != maxOutlineEntries {
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
