package script

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// marker stands for a credential in an argument: it must never reach an
// observation, whatever goes wrong.
const marker = "MARKER-s3cr3t"

// panicky is a Commands lookup that panics with the marker.
type panicky struct{}

func (panicky) Command(string) ([]string, bool, string) { panic("lookup " + marker) }

func TestCollectNeverReportsAnArgument(t *testing.T) {
	argv := []string{prog, "--password=" + marker, abs("nonexistent", marker), marker}
	cmds := fakeCommands{argv: map[string][]string{"x": argv}}
	runners := map[string]func(context.Context, []string) (runResult, error){
		"ok": func(context.Context, []string) (runResult, error) {
			return runResult{exited: true, stdout: []byte("OK | a=1")}, nil
		},
		"start failure naming the arguments": func(_ context.Context, argv []string) (runResult, error) {
			return runResult{}, fmt.Errorf("exec %s: failed", strings.Join(argv, " "))
		},
		"start failure wrapping an errno": func(_ context.Context, argv []string) (runResult, error) {
			return runResult{}, fmt.Errorf("exec %q: %w", argv, errors.New(marker))
		},
		"panic naming the arguments": func(_ context.Context, argv []string) (runResult, error) {
			panic(fmt.Sprint("run ", argv))
		},
		"panic with an error": func(context.Context, []string) (runResult, error) {
			panic(errors.New(marker))
		},
		"timeout": func(context.Context, []string) (runResult, error) {
			return runResult{}, fmt.Errorf("%w: %s", errTimeout, marker)
		},
		"busy": func(context.Context, []string) (runResult, error) {
			return runResult{}, fmt.Errorf("%w: %s", errBusy, marker)
		},
	}
	for _, format := range []string{"nagios", "json"} {
		for name, run := range runners {
			t.Run(format+"/"+name, func(t *testing.T) {
				p := newPlugin(&fakeRunner{run: run}, progOK())
				obs, err := p.CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}, "format": format}, cmds)
				assertNoMarker(t, obs, err)
				if len(obs) < 2 {
					t.Errorf("got %d observations, want at least status and output", len(obs))
				}
			})
		}
	}
	t.Run("refused program", func(t *testing.T) {
		c := &fakeChecker{files: map[string]error{prog: errors.New("owned by someone else")}}
		obs, err := newPlugin(exitedWith(0, ""), c).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, cmds)
		assertNoMarker(t, obs, err)
	})
	t.Run("argument the checker could not check", func(t *testing.T) {
		c := checkerFunc(func(path string) error {
			if path == prog {
				return nil
			}
			return errors.New("stat " + path + ": permission denied")
		})
		obs, err := newPlugin(exitedWith(0, ""), c).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, cmds)
		assertNoMarker(t, obs, err)
	})
	t.Run("program the checker could not check", func(t *testing.T) {
		c := checkerFunc(func(path string) error { return errors.New("stat " + path + " " + marker) })
		obs, err := newPlugin(exitedWith(0, ""), c).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, cmds)
		assertNoMarker(t, obs, err)
	})
	t.Run("refusal of an argument that is no file", func(t *testing.T) {
		// A checker breaking the contract: a refusal for a path that names
		// no file is still only echoed for what it checked, the argument
		// itself, never another one.
		c := checkerFunc(func(path string) error {
			if path == prog {
				return nil
			}
			return refuse("not owned by root")
		})
		argv := []string{prog, "--password=" + marker, abs("etc", "x.py")}
		obs, err := newPlugin(exitedWith(0, ""), c).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, fakeCommands{argv: map[string][]string{"x": argv}})
		assertNoMarker(t, obs, err)
	})
	t.Run("checker panics", func(t *testing.T) {
		c := checkerFunc(func(path string) error { panic("check " + path + " " + marker) })
		obs, err := newPlugin(exitedWith(0, ""), c).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, cmds)
		assertNoMarker(t, obs, err)
	})
}

type checkerFunc func(string) error

func (f checkerFunc) Check(path string) (string, error) { return path, f(path) }

func TestCollectRecoversPanics(t *testing.T) {
	t.Run("in the runner", func(t *testing.T) {
		r := &fakeRunner{run: func(_ context.Context, argv []string) (runResult, error) {
			if argv[1] == "boom" {
				panic("run " + marker)
			}
			return runResult{exited: true, stdout: []byte("fine")}, nil
		}}
		cmds := fakeCommands{argv: map[string][]string{"a": {prog, "boom", marker}, "b": {prog, "calm"}}}
		obs, err := newPlugin(r, progOK()).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"a", "b"}}, cmds)
		assertNoMarker(t, obs, err)
		equalLines(t, render(obs)[:3], []string{
			`a script.status "unknown"`, `a script.output "` + textInternal + `"`, `b script.status "ok"`,
		})
	})
	t.Run("in the lookup", func(t *testing.T) {
		obs, err := newPlugin(exitedWith(0, ""), progOK()).CollectWithCommands(context.Background(), map[string]any{"commands": []string{"x"}}, panicky{})
		assertNoMarker(t, obs, err)
		equalLines(t, render(obs), []string{`x script.status "unknown"`, `x script.output "` + textInternal + `"`})
	})
}

func assertNoMarker(t *testing.T, obs []checks.Observation, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), marker) {
		t.Errorf("the error holds an argument: %v", err)
	}
	for _, o := range obs {
		var label string
		if o.Label != nil {
			label = *o.Label
		}
		if strings.Contains(o.Item+o.Metric+o.Err+label, marker) {
			t.Errorf("an observation holds an argument: %+v (label %q)", o, label)
		}
	}
}
