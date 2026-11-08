package requirements_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

func TestEnsureCapsAndCleansGalaxyLines(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.Lines = []string{"cloning https://bob:tok3n@git.example.org/r.git"}
	for i := 2; i <= 503; i++ {
		g.Lines = append(g.Lines, fmt.Sprintf("line %d", i))
	}
	_, lines, err := c.ensure(newService(g), commitA)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// installing … / 500 galaxy lines / … 3 more lines / installed in …
	if len(lines) != 503 {
		t.Fatalf("got %d lines, want 503", len(lines))
	}
	galaxy := lines[1:501]
	if galaxy[0] != "cloning https://[redacted]@git.example.org/r.git" {
		t.Errorf("first galaxy line = %q", galaxy[0])
	}
	if !slices.Equal(galaxy[1:], g.Lines[1:500]) {
		t.Errorf("galaxy lines 2..500 differ: %q … %q", galaxy[1], galaxy[499])
	}
	if lines[501] != "… 3 more lines" {
		t.Errorf("line after the galaxy lines = %q, want %q", lines[501], "… 3 more lines")
	}
}

func TestEnsureCountsLinesPerCommand(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesAndCollections})
	g := newGalaxy()
	g.Lines = nil
	for i := 1; i <= 300; i++ {
		g.Lines = append(g.Lines, fmt.Sprintf("line %d", i))
	}
	_, lines, err := c.ensure(newService(g), commitA)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Two commands of 300 lines each stay under the cap of 500 per command.
	if len(lines) != 602 {
		t.Fatalf("got %d lines, want 602", len(lines))
	}
}

func TestEnsureCutsTheStderrTailAtACharacter(t *testing.T) {
	c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
	g := newGalaxy()
	g.Err = &requirements.ExitError{Code: 1, Stderr: strings.Repeat("é", 3000) + "x"}
	_, _, err := c.ensure(newService(g), commitA)
	prefix := "ansible-galaxy role install -r requirements.yml: exit 1: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("err = %v", err)
	}
	if tail := strings.TrimPrefix(err.Error(), prefix); tail != strings.Repeat("é", 2047)+"x" {
		t.Errorf("tail is %d bytes, valid UTF-8 %v; want 2047 é and x", len(tail), utf8.ValidString(tail))
	}

	g.Err = &requirements.ExitError{Code: 3, Stderr: "\n"}
	_, _, err = c.ensure(newService(g), commitA)
	assertErr(t, err, "ansible-galaxy role install -r requirements.yml: exit 3")
}

func TestSystemClockIsTheWallClock(t *testing.T) {
	before := time.Now()
	now := requirements.SystemClock{}.Now()
	if now.Before(before) || now.After(time.Now()) {
		t.Errorf("SystemClock.Now() = %v, want the wall clock", now)
	}
}

func TestEnsureRemovesUserinfoFromEveryError(t *testing.T) {
	cases := []struct {
		name, content string
		galaxyErr     error
		want          string
	}{
		{"a refused file", "- src: x\n  name: x\n  version: \"-https://bob:s3cret@git.example.org/v\"\n", nil,
			`requirements.yml: the version "-https://[redacted]@git.example.org/v" starts with "-"`},
		{"a galaxy error", rolesOnly, errors.New("clone https://bob:s3cret@git.example.org/r.git: refused"),
			"ansible-galaxy role install -r requirements.yml: clone https://[redacted]@git.example.org/r.git: refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClone(t, map[string]string{"requirements.yml": tc.content})
			g := newGalaxy()
			g.Err = tc.galaxyErr
			_, _, err := c.ensure(newService(g), commitA)
			assertErr(t, err, tc.want)
			if tc.galaxyErr != nil && !errors.Is(err, tc.galaxyErr) {
				t.Errorf("err %v does not wrap the galaxy error", err)
			}
		})
	}
}

// TestEnsureRemovesABareTokenUsername: a token used as a URL's username
// (https://TOKEN@host/…) never reaches the log or an error: not in a
// streamed line, not in galaxy's stderr tail, not in a galaxy error.
func TestEnsureRemovesABareTokenUsername(t *testing.T) {
	const want = "https://[redacted]@git.example.org/r.git"
	t.Run("a streamed line", func(t *testing.T) {
		c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
		g := newGalaxy()
		g.Lines = []string{"cloning https://ghp_s3cret@git.example.org/r.git"}
		_, lines, err := c.ensure(newService(g), commitA)
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if len(lines) < 2 || lines[1] != "cloning "+want {
			t.Errorf("lines = %q, want the second to be %q", lines, "cloning "+want)
		}
		for _, l := range lines {
			if strings.Contains(l, "ghp_s3cret") {
				t.Errorf("line leaks the token: %q", l)
			}
		}
	})
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"the stderr tail of a failure", &requirements.ExitError{Code: 1, Stderr: "fatal: https://ghp_s3cret@git.example.org/r.git not found"}},
		{"a galaxy error", errors.New("clone https://ghp_s3cret@git.example.org/r.git: refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClone(t, map[string]string{"requirements.yml": rolesOnly})
			g := newGalaxy()
			g.Err = tc.err
			_, _, err := c.ensure(newService(g), commitA)
			if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "ghp_s3cret") {
				t.Errorf("err = %v, want it to contain %q and not the token", err, want)
			}
		})
	}
}
