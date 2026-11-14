package script

import (
	"encoding/json"
	"math"
	"time"
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

// options are a check's parsed format and timeout_seconds.
type options struct {
	format  format
	timeout time.Duration
}

// parseOptions reads format (absent: nagios) and timeout_seconds (absent:
// 10). The manager validates both, so a bad value is a manager bug; it
// comes back as the fixed text every command of the check then reports.
func parseOptions(params map[string]any) (options, string) {
	opts := options{format: formatNagios, timeout: defaultTimeout}
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
