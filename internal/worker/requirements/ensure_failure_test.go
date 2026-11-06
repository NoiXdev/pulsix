package requirements_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/worker/requirements"
	"github.com/noixdev/pulsix/internal/worker/requirements/requirementstest"
)

// assertFailedClean checks that a failed install left no entry and no temp
// dir, and that the temp entry galaxy was given was removed through it.
func assertFailedClean(t *testing.T, c clone, g *requirementstest.Galaxy) {
	t.Helper()
	c.assertNothingLeft()
	calls := g.Calls()
	if len(calls) == 0 {
		t.Fatal("galaxy was not called")
	}
	if !slices.Contains(g.Removed(), calls[0].Entry) {
		t.Errorf("Removed() = %q, want the temp entry %q", g.Removed(), calls[0].Entry)
	}
}

func TestEnsureFailureLeavesNoEntry(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.Err = &requirements.ExitError{Code: 1, Stderr: "fetching https://bob:s3cret@git.example.org/r.git\nERROR! boom"}
	_, _, err := c.ensure(newService(g), commitA)
	assertErr(t, err, "ansible-galaxy role install -r requirements.yml: exit 1: fetching https://[redacted]@git.example.org/r.git\nERROR! boom")
	var exit *requirements.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Errorf("errors.As(ExitError) = %+v", exit)
	}
	assertFailedClean(t, c, g)

	g.Err = &requirements.ExitError{Code: 2, Stderr: "head " + strings.Repeat("x", 10*1024) + "\n"}
	_, _, err = c.ensure(newService(g), commitA)
	prefix := "ansible-galaxy role install -r requirements.yml: exit 2: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("err = %v, want the prefix %q", err, prefix)
	}
	if tail := strings.TrimPrefix(err.Error(), prefix); tail != strings.Repeat("x", 4096) {
		t.Errorf("tail is %d bytes, want 4096 x", len(tail))
	}
	c.assertNothingLeft()
}

func TestEnsureReportsAnotherGalaxyError(t *testing.T) {
	c := newClone(t, map[string]string{"collections/requirements.yml": collectionsOnly})
	g := newGalaxy()
	g.Err = errors.New("start ansible-galaxy: no such file")
	_, _, err := c.ensure(newService(g), commitA)
	assertErr(t, err, "ansible-galaxy collection install -r collections/requirements.yml: start ansible-galaxy: no such file")
	assertFailedClean(t, c, g)
}

func TestEnsureTimesOut(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.Hold = make(chan struct{})
	svc := requirements.NewService(g, &requirementstest.Clock{}, 50*time.Millisecond)
	_, _, err := c.ensure(svc, commitA)
	assertErr(t, err, "ansible-galaxy role install -r requirements.yml: timed out after 50ms")
	if errors.Is(err, context.Canceled) {
		t.Errorf("err %v is context.Canceled", err)
	}
	assertFailedClean(t, c, g)
}

func TestEnsureCancelled(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.Hold = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	g.OnInstall = func(requirements.Install) error {
		cancel()
		return nil
	}
	_, err := newService(g).Ensure(ctx, c.request(commitA, c.files(""), nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertErr(t, err, "ansible-galaxy role install -r requirements.yml: context canceled")
	assertFailedClean(t, c, g)
}

func TestEnsureRefusesAnInstallItCannotTakeOver(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.OnInstall = func(in requirements.Install) error {
		return os.Symlink("/etc", filepath.Join(in.Entry, "roles", "x"))
	}
	_, _, err := c.ensure(newService(g), commitA)
	assertErr(t, err, "take over the install: roles/x: a symlink leaving the install")
	assertFailedClean(t, c, g)
}

func TestEnsureRefusesBadFilesBeforeTheCache(t *testing.T) {
	cases := []struct {
		name, content, commit, want string
	}{
		{"include", "- include: x.yml\n", commitA,
			"requirements.yml: include: is not supported; list the roles in this file"},
		{"source outside", "- src: ../../x.tar.gz\n  name: x\n", commitA,
			`requirements.yml: the source "../../x.tar.gz" is outside the repository`},
		{"not a requirements file", "just text\n", commitA,
			"requirements.yml is not a requirements file: the top level must be a list of roles or a mapping of roles and collections"},
		{"larger than 512 KiB", padTo(rolesOnly, 512<<10+1), commitA,
			"requirements.yml is not a requirements file: larger than 512 KiB"},
		{"short commit", rolesOnly, "abc", `commit "abc" is not a full commit id`},
		{"upper-case commit", rolesOnly, strings.ToUpper(commitA), `commit "` + strings.ToUpper(commitA) + `" is not a full commit id`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClone(t, map[string]string{"requirements.yml": tc.content})
			g := newGalaxy()
			_, _, err := c.ensure(newService(g), tc.commit)
			assertErr(t, err, tc.want)
			if len(g.Calls()) > 0 {
				t.Errorf("galaxy was called: %+v", g.Calls())
			}
			c.assertNoCache()
		})
	}
}

// padTo is content followed by comment lines up to exactly n bytes.
func padTo(content string, n int) string {
	var b strings.Builder
	b.WriteString(content)
	for b.Len() < n {
		line := "# " + strings.Repeat("x", min(77, n-b.Len()-3)) + "\n"
		if n-b.Len() < 3 {
			line = strings.Repeat("\n", n-b.Len())
		}
		b.WriteString(line)
	}
	return b.String()
}

func TestEnsureAcceptsAFileOf512KiB(t *testing.T) {
	content := padTo(rolesOnly, 512<<10)
	if len(content) != 512<<10 {
		t.Fatalf("padTo made %d bytes", len(content))
	}
	c := newClone(t, map[string]string{"requirements.yml": content})
	if _, _, err := c.ensure(newService(newGalaxy()), commitA); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
}

func TestEnsureReportsAFileItCannotRead(t *testing.T) {
	c := newClone(t, nil)
	g := newGalaxy()
	files := requirements.Files{Paths: []string{"requirements.yml"}}
	_, err := newService(g).Ensure(t.Context(), c.request(commitA, files, nil))
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "requirements.yml") {
		t.Fatalf("err = %v, want a not-exist error naming the file", err)
	}
	c.assertNoCache()
}

func TestEnsureSha256Commit(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	commit := strings.Repeat("ab", 32)
	entry, _, err := c.ensure(newService(newGalaxy()), commit)
	if err != nil || !strings.HasPrefix(filepath.Base(entry.Dir), commit+"-") {
		t.Fatalf("Ensure = %+v, %v", entry, err)
	}
}
