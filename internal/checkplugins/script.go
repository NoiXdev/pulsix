package checkplugins

import (
	"fmt"
	"slices"
	"strings"

	"github.com/noixdev/pulsix/internal/scriptargs"
)

// Script plugin bounds (backlog 3.6g). The agent trusts params that pass
// here, so these and internal/agent/checks/plugins/script/ agree: at most
// maxCommands names, timeout_seconds within minTimeoutSecs and
// maxTimeoutSecs, format nagios or json.
const (
	maxScriptCommands   = 32
	maxScriptArgs       = 64
	minScriptTimeoutSec = 1
	maxScriptTimeoutSec = 60
	defScriptTimeoutSec = 10
)

// scriptPlugin: commands the host defines under commands: in its
// agent.yaml (backlog 3.6g). A check only names them — FormatCommandName
// admits a name, never a path or an argument — so nothing the manager
// holds can change what runs on the host. Each command is an item; a
// value it reports is "<command>:<label>", so ItemEntry cuts an item at
// its first ':' (a command name never contains one).
//
// args (backlog 3.6j) are values for placeholders a command's definition
// on the host declares, "command:placeholder=value". The manager checks
// their shape only (scriptargs); the host's rules, which the manager does
// not know, judge each value on the agent, and a command without
// placeholders refuses any.
func scriptPlugin() Descriptor {
	return Descriptor{
		Name: "script",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "commands", Type: TypeList, Required: true, NonEmpty: true, MaxEntries: maxScriptCommands, Format: FormatCommandName},
			{Name: "args", Type: TypeList, MaxEntries: maxScriptArgs, MaxBytes: scriptargs.MaxEntryBytes, Format: FormatScriptArg, Example: "tcp_port:port=8080"},
			{Name: "format", Type: TypeEnum, Options: []string{"nagios", "json"}, Default: "nagios"},
			{Name: "timeout_seconds", Type: TypeInteger, Min: bound(minScriptTimeoutSec), Max: bound(maxScriptTimeoutSec), Default: int64(defScriptTimeoutSec)},
		},
		ItemParam: "commands",
		Check:     scriptCheck,
		ItemEntry: scriptItemCommand,
		Metrics: []MetricSpec{
			{Name: "script.status", Kind: Discrete},
			{Name: "script.output", Kind: Discrete},
			{Name: "script.exit_code", Kind: Numeric},
			{Name: "script.duration_seconds", Kind: Numeric},
			// Item "<command>:<label>": one perfdata or JSON value.
			{Name: "script.value", Kind: Numeric},
		},
	}
}

// scriptItemCommand is the command a script item belongs to: the item
// itself, or what precedes the first ':' of a value's item.
func scriptItemCommand(item string) string {
	command, _, _ := strings.Cut(item, ":")
	return command
}

// scriptCheck: every args entry is for a command the check lists, sets a
// placeholder once and gives no command more values than a command can
// take. Errors name the command and placeholder, never a value.
func scriptCheck(params map[string]any) error {
	commands, _ := stringList(params["commands"])
	args, _ := stringList(params["args"])
	set := map[string]bool{}
	perCommand := map[string]int{}
	for _, entry := range args {
		command, name, _, _ := scriptargs.ParseEntry(entry)
		switch {
		case !slices.Contains(commands, command):
			return fmt.Errorf("args gives a value to command %q, which commands does not list", command)
		case set[command+":"+name]:
			return fmt.Errorf("args sets %s:%s twice", command, name)
		}
		set[command+":"+name] = true
		perCommand[command]++
		if perCommand[command] > scriptargs.MaxPlaceholders {
			return fmt.Errorf("args gives command %q more than %d values", command, scriptargs.MaxPlaceholders)
		}
	}
	return nil
}
