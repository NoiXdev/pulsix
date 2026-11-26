package varsource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// vaultNamesOf is the report's vault names as name@file.
func vaultNamesOf(v *Vault) []string {
	out := []string{}
	for _, n := range v.Names {
		out = append(out, n.Name+"@"+n.File)
	}
	return out
}

// writeDir writes files (slash paths) under root.
func writeDir(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestVaultNamesFollowLinks: a linked file or directory is read as Ansible
// reads it, under the path that links to it: a link inside group_vars and
// a group_vars that is itself a link. A loop of links ends; a link that
// cannot be followed is unread, so the read is not complete.
func TestVaultNamesFollowLinks(t *testing.T) {
	root := t.TempDir()
	writeDir(t, root, map[string]string{
		"shared/web.yml":        "linked_dir_secret: " + vaultTag,
		"other/all.yml":         "linked_group_vars_secret: " + vaultTag,
		"files/db.yml":          "linked_file_secret: " + vaultString,
		"playbooks/site.yml":    "- hosts: all\n",
		"group_vars/plain.yml":  "plain: 1\n",
		"group_vars/direct.yml": "direct_secret: " + vaultTag,
	})
	symlink(t, filepath.Join("..", "shared"), filepath.Join(root, "group_vars", "web"))
	symlink(t, filepath.Join("..", "files", "db.yml"), filepath.Join(root, "group_vars", "db.yml"))
	symlink(t, filepath.Join("..", "other"), filepath.Join(root, "playbooks", "group_vars"))
	symlink(t, ".", filepath.Join(root, "group_vars", "loop"))

	r := ParseRepo(context.Background(), os.DirFS(root), "c")
	want := []string{
		"direct_secret@group_vars/direct.yml",
		"linked_dir_secret@group_vars/web/web.yml",
		"linked_file_secret@group_vars/db.yml",
		"linked_group_vars_secret@playbooks/group_vars/all.yml",
	}
	if got := vaultNamesOf(r.Vault); !reflect.DeepEqual(got, want) {
		t.Fatalf("names =\n%q\nwant\n%q", got, want)
	}
	if !r.Vault.Complete() {
		t.Fatalf("vault = %+v, want a complete read", r.Vault)
	}

	// A link that leads nowhere is not followed: listed, not complete.
	symlink(t, filepath.Join("..", "gone"), filepath.Join(root, "host_vars"))
	symlink(t, filepath.Join("..", "missing.yml"), filepath.Join(root, "group_vars", "missing.yml"))
	symlink(t, filepath.Join("..", "nowhere"), filepath.Join(root, "docs"))
	r = ParseRepo(context.Background(), os.DirFS(root), "c")
	if want := []string{"group_vars/missing.yml", "host_vars"}; !reflect.DeepEqual(r.Vault.Unread, want) || r.Vault.Complete() {
		t.Fatalf("unread = %q, complete %v; want %q, not complete (docs holds no variables)", r.Vault.Unread, r.Vault.Complete(), want)
	}
}

// TestVaultNamesIncompleteWhereTheReadCannotFollow: a vars_files entry the
// read cannot follow (templated, a first-found list, outside the subpath,
// absolute), a value nested past the depth limit and a mapping merging
// more sources than are read each leave the names not all known.
func TestVaultNamesIncompleteWhereTheReadCannotFollow(t *testing.T) {
	deep := "deep: " + strings.Repeat("[", maxVaultDepth+2) + "1" + strings.Repeat("]", maxVaultDepth+2) + "\n"
	// The file's own keys merged from more sources than are read: the
	// last one's keys are not known.
	var merge strings.Builder
	merge.WriteString("defs:\n")
	for i := range maxMergeSources + 1 {
		fmt.Fprintf(&merge, "  - &a%d {k%d: 1}\n", i, i)
	}
	merge.WriteString("<<: [")
	for i := range maxMergeSources + 1 {
		if i > 0 {
			merge.WriteString(", ")
		}
		fmt.Fprintf(&merge, "*a%d", i)
	}
	merge.WriteString("]\n")
	for _, tc := range []struct {
		name  string
		files map[string]string
		cut   bool
	}{
		{"static vars_files", map[string]string{"site.yml": "- hosts: all\n  vars_files: [extra.yml]\n", "extra.yml": "a: 1\n"}, false},
		{"templated vars_files", map[string]string{"site.yml": "- hosts: all\n  vars_files: [\"{{ env }}.yml\"]\n"}, true},
		{"first-found vars_files", map[string]string{"site.yml": "- hosts: all\n  vars_files:\n    - [a.yml, b.yml]\n"}, true},
		{"vars_files outside the subpath", map[string]string{"site.yml": "- hosts: all\n  vars_files: [../x.yml]\n"}, true},
		{"absolute vars_files", map[string]string{"site.yml": "- hosts: all\n  vars_files: [/etc/x.yml]\n"}, true},
		{"nested too deep in a variable file", map[string]string{"group_vars/all.yml": deep}, true},
		{"nested too deep in play vars", map[string]string{"site.yml": "- hosts: all\n  vars:\n    " + deep}, true},
		{"too many merge sources", map[string]string{"group_vars/all.yml": merge.String()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseRepo(context.Background(), mapFS(tc.files), "c")
			if r.Vault.Cut != tc.cut || r.Vault.Complete() == tc.cut {
				t.Fatalf("vault = %+v (notes %v), want cut %v", r.Vault, r.Notes, tc.cut)
			}
		})
	}
}

// countdownCtx ends after a number of Err calls: a deadline that passes
// in the middle of the walk, without a clock.
type countdownCtx struct {
	context.Context
	left int
}

func (c *countdownCtx) Err() error {
	if c.left <= 0 {
		return context.DeadlineExceeded
	}
	c.left--
	return nil
}

// TestVaultWalkStopsAtTheTimeLimitBetweenEntries: the time limit is
// checked at every directory entry, so a large tree without one variable
// file does not keep the walk going past it.
func TestVaultWalkStopsAtTheTimeLimitBetweenEntries(t *testing.T) {
	files := map[string]string{"zz/group_vars/all.yml": "late: " + vaultTag}
	for i := range 3000 {
		files[fmt.Sprintf("docs/d%02d/f%04d.md", i%30, i)] = "x"
	}
	v := newVaultSet(&countdownCtx{Context: context.Background(), left: 50})
	v.scanVaultFiles(mapFS(files), Installed{})
	got := v.result(false)
	if !got.Cut || len(got.Names) != 0 {
		t.Fatalf("vault = %+v, want a cut read", got)
	}
	if v.entries > 60 {
		t.Fatalf("%d entries visited after the time limit passed, want the walk to stop", v.entries)
	}
}
