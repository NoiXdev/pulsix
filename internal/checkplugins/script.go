package checkplugins

import "strings"

// Script plugin bounds (backlog 3.6g). The agent trusts params that pass
// here, so these and internal/agent/checks/plugins/script/ agree: at most
// maxCommands names, timeout_seconds within minTimeoutSecs and
// maxTimeoutSecs, format nagios or json.
const (
	maxScriptCommands   = 32
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
func scriptPlugin() Descriptor {
	return Descriptor{
		Name: "script",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "commands", Type: TypeList, Required: true, NonEmpty: true, MaxEntries: maxScriptCommands, Format: FormatCommandName},
			{Name: "format", Type: TypeEnum, Options: []string{"nagios", "json"}, Default: "nagios"},
			{Name: "timeout_seconds", Type: TypeInteger, Min: bound(minScriptTimeoutSec), Max: bound(maxScriptTimeoutSec), Default: int64(defScriptTimeoutSec)},
		},
		ItemParam: "commands",
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
