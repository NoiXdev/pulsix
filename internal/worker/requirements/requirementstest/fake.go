// Package requirementstest holds fakes of the requirements service's
// dependencies: a Galaxy that writes a fixed install without running
// ansible-galaxy, and a Clock that advances by a fixed step. It also holds
// the galaxy tarball fixtures (TarGz, RoleTarball, EvilCollectionTarball)
// the runner, e2e and in-image tests share.
package requirementstest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/noixdev/pulsix/internal/worker/requirements"
)

// Galaxy is a fake requirements.Galaxy. Every Install call is recorded,
// then runs OnInstall, waits for Hold, writes Roles or Collections, sends
// Lines and returns Err. It is safe for concurrent use; set the fields
// before the first call.
type Galaxy struct {
	// Roles and Collections are written below <Entry>/roles or
	// <Entry>/collections by a role or collection install: slash path to
	// content (directories 0700, files 0600).
	Roles, Collections map[string]string
	// Lines are sent through line on every call.
	Lines []string
	// Err is returned by every call after the lines.
	Err error
	// Hold, when set, makes every call wait until it is closed or ctx
	// ends; then the call returns ctx.Err().
	Hold chan struct{}
	// OnInstall runs first on every call; its error is returned.
	OnInstall func(in requirements.Install) error
	// RemoveErr, when set, makes RemoveAll record the directory and
	// return RemoveErr without removing anything.
	RemoveErr error

	mu      sync.Mutex
	calls   []requirements.Install
	removed []string
}

// Install records in and plays the fake's install.
func (g *Galaxy) Install(ctx context.Context, in requirements.Install, line func(string)) error {
	g.mu.Lock()
	g.calls = append(g.calls, in)
	g.mu.Unlock()
	if g.OnInstall != nil {
		if err := g.OnInstall(in); err != nil {
			return err
		}
	}
	if g.Hold != nil {
		select {
		case <-g.Hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	files, dir := g.Roles, "roles"
	if in.Kind == requirements.KindCollection {
		files, dir = g.Collections, "collections"
	}
	for name, content := range files {
		p := filepath.Join(in.Entry, dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return err
		}
	}
	for _, l := range g.Lines {
		line(l)
	}
	return g.Err
}

// RemoveAll removes dir with os.RemoveAll (unless RemoveErr is set) and
// records it.
func (g *Galaxy) RemoveAll(dir string) error {
	g.mu.Lock()
	g.removed = append(g.removed, dir)
	g.mu.Unlock()
	if g.RemoveErr != nil {
		return g.RemoveErr
	}
	return os.RemoveAll(dir)
}

// Calls returns the installs so far, in call order.
func (g *Galaxy) Calls() []requirements.Install {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

// Removed returns the directories RemoveAll was given, in call order.
func (g *Galaxy) Removed() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.removed)
}

// start is when a Clock with a zero T begins: a zero time would leave a
// file's time unchanged in os.Chtimes.
var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Clock is a fake requirements.Clock: Now returns T, then advances T by
// Step. A zero T starts at 2026-01-01 00:00 UTC. It is safe for concurrent
// use.
type Clock struct {
	T    time.Time
	Step time.Duration

	mu sync.Mutex
}

// Now returns the current fake time and advances it.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.T.IsZero() {
		c.T = start
	}
	now := c.T
	c.T = c.T.Add(c.Step)
	return now
}
