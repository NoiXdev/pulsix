package script

import (
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/scriptargs"
)

// format is how a command's output is read.
type format string

const (
	formatNagios format = "nagios"
	formatJSON   format = "json"
)

const (
	defaultTimeout = 10 * time.Second
	minTimeoutSecs = 1
	maxTimeoutSecs = 60
)

// options are a check's parsed format, timeout_seconds and args.
type options struct {
	format  format
	timeout time.Duration
	// args are the values per command and placeholder (backlog 3.6j);
	// a command without values has none.
	args map[string]map[string]string
}

// parseOptions reads format (absent: nagios), timeout_seconds (absent:
// 10) and args (absent: no values) for a check listing names. The manager
// validates them, so a bad value is a manager bug; it comes back as the
// fixed text every command of the check then reports.
func parseOptions(params map[string]any, names []string) (options, string) {
	opts := options{format: formatNagios, timeout: defaultTimeout}
	args, ok := parseArgs(params, names)
	if !ok {
		return options{}, textBadArgs
	}
	opts.args = args
	if v := params["format"]; v != nil {
		s, _ := v.(string)
		switch format(s) {
		case formatNagios, formatJSON:
			opts.format = format(s)
		default:
			return options{}, textBadFormat
		}
	}
	if v := params["timeout_seconds"]; v != nil {
		secs, ok := wholeNumber(v)
		if !ok || secs < minTimeoutSecs || secs > maxTimeoutSecs {
			return options{}, textBadTimeout
		}
		opts.timeout = time.Duration(secs) * time.Second
	}
	return opts, ""
}

// wholeNumber reads the shapes an integer param arrives in: an int from
// the agent's own callers, a float64 or json.Number after a JSON round
// trip.
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// parseArgs reads args, entries "command:placeholder=value", into values
// per command and placeholder. Like the manager it refuses an entry of the
// wrong shape (scriptargs.EntryProblem), one for a command the check does
// not list and a placeholder set twice for one command. Whether the
// command takes the value, and whether the value passes, is the host's
// rule (scriptargs.Template.Bind).
func parseArgs(params map[string]any, names []string) (map[string]map[string]string, bool) {
	if params["args"] == nil {
		return nil, true
	}
	list, _, err := checks.ParamStrings(params, "args")
	if err != nil {
		return nil, false
	}
	out := map[string]map[string]string{}
	for _, entry := range list {
		if scriptargs.EntryProblem(entry) != "" {
			return nil, false
		}
		command, name, value, _ := scriptargs.ParseEntry(entry)
		if !slices.Contains(names, command) {
			return nil, false
		}
		if out[command] == nil {
			out[command] = map[string]string{}
		}
		if _, dup := out[command][name]; dup {
			return nil, false
		}
		out[command][name] = value
	}
	return out, true
}
