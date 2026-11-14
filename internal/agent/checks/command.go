package checks

import "context"

// CommandPlugin is a Plugin that runs commands defined on the host
// (backlog 3.6g). The scheduler calls CollectWithCommands instead of
// Collect, with the check's params exactly as they are and a lookup for
// the commands the agent's configuration file (agent.yaml) defines. Only
// command names travel from the manager; what a name runs comes from the
// file through the lookup. The plugin never reads the file itself.
//
// A command's arguments may hold credentials: the plugin must not put an
// argument other than the program path into an observation or its
// advisory error. The scheduler drops a CommandPlugin's advisory error
// rather than logging it, so the observations carry every failure that
// matters.
//
// A plugin that is both a CommandPlugin and a SecretPlugin is called as a
// CommandPlugin.
type CommandPlugin interface {
	Plugin
	CollectWithCommands(ctx context.Context, params map[string]any, commands Commands) ([]Observation, error)
}

// Commands is where a CommandPlugin looks a command up: the agent's
// secrets file watcher, adapted in internal/agent. ok is false when the
// file does not define name; problem then says why the file has no
// commands at all (missing, refused, broken), or is empty when only this
// one is missing. argv is the program followed by its arguments, a copy
// the caller may keep.
type Commands interface {
	Command(name string) (argv []string, ok bool, problem string)
}

// WithCommands is where CommandPlugins look their commands up; the
// scheduler hands them c as it is. Without it, or with a nil c, they get a
// lookup that defines no command and names no problem.
func WithCommands(c Commands) SchedulerOption {
	return func(sc *Scheduler) {
		if c != nil {
			sc.commands = c
		}
	}
}

// noCommands is the lookup of a scheduler without WithCommands.
type noCommands struct{}

func (noCommands) Command(string) ([]string, bool, string) { return nil, false, "" }
