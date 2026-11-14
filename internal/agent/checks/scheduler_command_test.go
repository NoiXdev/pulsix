package checks

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// commandArg is a command argument no log line may ever carry.
const commandArg = "arg-Pa55word-77"

// fakeCommandPlugin looks up the command its params name through the
// lookup it was handed, reports what it found and returns the argv as its
// advisory error, so a test sees whether the scheduler logs it.
type fakeCommandPlugin struct {
	mu         sync.Mutex
	plain      int
	calls      int
	lastParams map[string]any
	lookup     Commands
	argv       []string
	ok         bool
	problem    string
}

func (p *fakeCommandPlugin) Name() string { return "db" }

func (p *fakeCommandPlugin) Collect(context.Context, map[string]any) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.plain++
	return []Observation{ValueObservation("", "script.plain", 1)}, nil
}

func (p *fakeCommandPlugin) CollectWithCommands(_ context.Context, params map[string]any, commands Commands) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastParams = maps.Clone(params)
	p.lookup = commands
	name, _ := params["command"].(string)
	p.argv, p.ok, p.problem = commands.Command(name)
	return []Observation{ValueObservation(name, "script.exit_code", 0)},
		errors.New("ran " + strings.Join(p.argv, " "))
}

func (p *fakeCommandPlugin) seen() (plain, calls int, params map[string]any, lookup Commands, argv []string, ok bool, problem string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.plain, p.calls, p.lastParams, p.lookup, p.argv, p.ok, p.problem
}

// fakeCommands is a Commands a test fills in.
type fakeCommands struct {
	commands map[string][]string
	problem  string
}

func (f *fakeCommands) Command(name string) ([]string, bool, string) {
	argv, ok := f.commands[name]
	if !ok {
		return nil, false, f.problem
	}
	return slices.Clone(argv), true, ""
}

func TestSchedulerHandsACommandPluginTheLookup(t *testing.T) {
	commands := &fakeCommands{commands: map[string][]string{"disk": {"/usr/lib/check_disk", "-p", commandArg}}}
	plugin := &fakeCommandPlugin{}
	params := map[string]any{"command": "disk", "format": "nagios"}
	tick := secretRun(t, plugin, params, WithCommands(commands))

	results := tick()
	plain, calls, gotParams, lookup, argv, ok, problem := plugin.seen()
	if plain != 0 || calls != 1 {
		t.Fatalf("Collect calls = %d, CollectWithCommands calls = %d; want 0 and 1", plain, calls)
	}
	if lookup != Commands(commands) {
		t.Fatalf("lookup = %#v, want the configured one", lookup)
	}
	if want := []string{"/usr/lib/check_disk", "-p", commandArg}; !ok || problem != "" || !reflect.DeepEqual(argv, want) {
		t.Fatalf("Command = %q, %v, %q; want %q", argv, ok, problem, want)
	}
	if want := map[string]any{"command": "disk", "format": "nagios"}; !reflect.DeepEqual(gotParams, want) || !reflect.DeepEqual(params, want) {
		t.Fatalf("params = %v (spec %v), want %v", gotParams, params, want)
	}
	if len(results) != 1 || results[0].Item != "disk" || results[0].Metric != "script.exit_code" {
		t.Fatalf("results = %+v, want the plugin's one", results)
	}
}

// TestSchedulerPassesTheLookupsProblem: the plugin sees why the file has
// no commands, exactly as the lookup says it.
func TestSchedulerPassesTheLookupsProblem(t *testing.T) {
	commands := &fakeCommands{problem: "the file /etc/pulsix/agent.yaml does not exist"}
	plugin := &fakeCommandPlugin{}
	tick := secretRun(t, plugin, map[string]any{"command": "disk"}, WithCommands(commands))
	tick()
	if _, _, _, _, argv, ok, problem := plugin.seen(); ok || argv != nil || problem != commands.problem {
		t.Fatalf("Command = %q, %v, %q; want nothing and %q", argv, ok, problem, commands.problem)
	}
}

// TestSchedulerWithoutCommands: a scheduler nobody gave a commands source
// (or a nil one) still calls the plugin, with a lookup that defines
// nothing and names no problem.
func TestSchedulerWithoutCommands(t *testing.T) {
	for name, opts := range map[string][]SchedulerOption{"none": nil, "nil": {WithCommands(nil)}} {
		t.Run(name, func(t *testing.T) {
			plugin := &fakeCommandPlugin{}
			tick := secretRun(t, plugin, map[string]any{"command": "disk"}, opts...)
			tick()
			plain, calls, _, lookup, argv, ok, problem := plugin.seen()
			if plain != 0 || calls != 1 || lookup == nil {
				t.Fatalf("Collect %d, CollectWithCommands %d, lookup %v; want 0, 1, a lookup", plain, calls, lookup)
			}
			if ok || argv != nil || problem != "" {
				t.Fatalf("Command = %q, %v, %q; want nothing and no problem", argv, ok, problem)
			}
		})
	}
}

// TestSchedulerDoesNotLogACommandPluginsAdvisory: a command plugin's
// advisory error may carry an argument, so it never reaches the log.
func TestSchedulerDoesNotLogACommandPluginsAdvisory(t *testing.T) {
	buf := &syncBuf{}
	log := slog.New(slog.NewTextHandler(buf, nil))
	commands := &fakeCommands{commands: map[string][]string{"disk": {"/usr/lib/check_disk", commandArg}}}
	plugin := &fakeCommandPlugin{}
	tick := secretRun(t, plugin, map[string]any{"command": "disk"}, WithCommands(commands), WithLogger(log))
	tick()
	tick()
	if _, calls, _, _, _, _, _ := plugin.seen(); calls != 2 {
		t.Fatalf("CollectWithCommands calls = %d, want 2", calls)
	}
	if text := buf.String(); strings.Contains(text, commandArg) || strings.Contains(text, "advisory") {
		t.Fatalf("the log carries the plugin's advisory:\n%s", text)
	}
}
