package caddy

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

// metrics is what the page says of Caddy's HTTP handling, aggregated as the
// spec states: per server the largest value across the handler labels, then
// the sum of the servers. A have flag is false when the page carries no
// usable series of the family (Caddy without the metrics option).
type metrics struct {
	inflight     float64
	haveInflight bool
	requests     uint64
	haveRequests bool
	// errs5xx is the counter of responses with a code of 500-599, summed
	// over the codes of each (server, handler) pair before the largest
	// handler is taken.
	errs5xx uint64
	have5xx bool
}

const (
	// maxLine bounds one line of the page. Caddy's lines are short (a
	// label set and a number); the page itself is bounded by webcheck.
	maxLine = 64 << 10
	// maxCounter is the largest counter the plugin accepts: 2^53, where a
	// float64 still counts exactly. A series above it (or a sum above it)
	// is ignored or clamped, never wrapped.
	maxCounter = 1 << 53

	famInflight = "caddy_http_requests_in_flight"
	famRequests = "caddy_http_requests_total"
	famDuration = "caddy_http_request_duration_seconds_count"
)

// key is a (server, handler) pair; a series without the label has "".
type key struct{ server, handler string }

// aggregate collects the series of one page before they are folded.
type aggregate struct {
	inflight map[key]uint64
	requests map[key]uint64
	errs     map[key]uint64
	haveDur  bool
}

// parseMetrics reads the Prometheus text exposition format line by line:
// comments and blank lines are skipped, every other line must be a
// plausible sample, "name[{labels}] value [timestamp]" with a valid metric
// name and a numeric value; anything else (an HTML or plain-text page) is a
// format error. Only the three families the plugin reports are parsed in
// detail and bounded to 64 KiB per line; the labels of every other line are
// only skipped over. A sample whose value is NaN, infinite or negative, or
// above 2^53, is ignored.
func parseMetrics(body []byte) (metrics, error) {
	a := aggregate{
		inflight: map[key]uint64{},
		requests: map[key]uint64{},
		errs:     map[key]uint64{},
	}
	samples := 0
	rest := string(body)
	for rest != "" {
		line, tail, _ := strings.Cut(rest, "\n")
		rest = tail
		line = strings.TrimLeft(strings.TrimRight(line, " \t\r"), " \t")
		if line == "" || line[0] == '#' {
			continue
		}
		end := strings.IndexAny(line, " \t{")
		if end < 0 || !validName(line[:end]) {
			return metrics{}, webcheck.ErrFormat
		}
		samples++
		name := line[:end]
		if name != famInflight && name != famRequests && name != famDuration {
			after, ok := skipLabels(strings.TrimLeft(line[end:], " \t"))
			if !ok {
				return metrics{}, webcheck.ErrFormat
			}
			if _, err := parseValue(after); err != nil {
				return metrics{}, err
			}
			continue
		}
		if len(line) > maxLine {
			return metrics{}, webcheck.ErrFormat
		}
		if err := a.add(name, line[end:]); err != nil {
			return metrics{}, err
		}
	}
	if samples == 0 {
		return metrics{}, webcheck.ErrFormat
	}
	return a.fold(), nil
}

// skipLabels returns what follows the label set at the start of s (s itself
// without one), stepping over quoted values and their escapes without
// building anything; ok is false when the braces are not closed.
func skipLabels(s string) (rest string, ok bool) {
	if !strings.HasPrefix(s, "{") {
		return s, true
	}
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case inQuote && c == '\\':
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == '}':
			return s[i+1:], true
		}
	}
	return "", false
}

// parseValue reads "value [timestamp]": the value a float (an out-of-range
// one is infinite), the timestamp an integer, nothing after them.
func parseValue(s string) (float64, error) {
	val, tail, _ := cutField(s)
	if val == "" {
		return 0, webcheck.ErrFormat
	}
	if ts, more, _ := cutField(tail); ts != "" {
		if _, err := strconv.ParseInt(ts, 10, 64); err != nil || strings.TrimSpace(more) != "" {
			return 0, webcheck.ErrFormat
		}
	}
	v, err := strconv.ParseFloat(val, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, webcheck.ErrFormat
	}
	return v, nil
}

// cutField splits off the first blank-separated field of s.
func cutField(s string) (field, rest string, found bool) {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, "", s != ""
	}
	return s[:i], s[i:], true
}

// add parses the rest of a relevant line (labels, value, timestamp).
func (a *aggregate) add(name, rest string) error {
	var server, handler, code string
	rest = strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(rest, "{") {
		var err error
		if server, handler, code, rest, err = parseLabels(rest); err != nil {
			return err
		}
	}
	v, err := parseValue(rest)
	if err != nil {
		return err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxCounter {
		return nil
	}
	k := key{server, handler}
	n := uint64(v)
	switch name {
	case famInflight:
		a.inflight[k] = satAdd(a.inflight[k], n)
	case famRequests:
		a.requests[k] = satAdd(a.requests[k], n)
	case famDuration:
		a.haveDur = true
		if c, err := strconv.Atoi(code); err == nil && c >= 500 && c <= 599 {
			a.errs[k] = satAdd(a.errs[k], n)
		}
	}
	return nil
}

// fold takes the largest handler per server and sums the servers.
func (a *aggregate) fold() metrics {
	return metrics{
		inflight: float64(sumOfLargest(a.inflight)), haveInflight: len(a.inflight) > 0,
		requests: sumOfLargest(a.requests), haveRequests: len(a.requests) > 0,
		errs5xx: sumOfLargest(a.errs), have5xx: a.haveDur,
	}
}

func sumOfLargest(series map[key]uint64) uint64 {
	perServer := map[string]uint64{}
	for k, v := range series {
		perServer[k.server] = max(perServer[k.server], v)
	}
	var total uint64
	for _, v := range perServer {
		total = satAdd(total, v)
	}
	return total
}

// satAdd adds without ever exceeding maxCounter.
func satAdd(a, b uint64) uint64 {
	if a > maxCounter-b {
		return maxCounter
	}
	return a + b
}

// validName reports whether s is a Prometheus metric name.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// parseLabels reads "{name="value",...}" from the start of s and returns
// the three labels the plugin uses and what follows the closing brace.
func parseLabels(s string) (server, handler, code, rest string, err error) {
	s = s[1:]
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return "", "", "", "", webcheck.ErrFormat
		}
		if s[0] == '}' {
			return server, handler, code, s[1:], nil
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return "", "", "", "", webcheck.ErrFormat
		}
		label := strings.TrimSpace(s[:eq])
		if !validLabelName(label) {
			return "", "", "", "", webcheck.ErrFormat
		}
		s = strings.TrimLeft(s[eq+1:], " \t")
		if s == "" || s[0] != '"' {
			return "", "", "", "", webcheck.ErrFormat
		}
		var val string
		if val, s, err = parseQuoted(s[1:]); err != nil {
			return "", "", "", "", err
		}
		switch label {
		case "server":
			server = val
		case "handler":
			handler = val
		case "code":
			code = val
		}
		s = strings.TrimLeft(s, " \t")
		switch {
		case s != "" && s[0] == ',':
			s = s[1:]
		case s != "" && s[0] == '}':
		default:
			return "", "", "", "", webcheck.ErrFormat
		}
	}
}

// parseQuoted reads a label value up to its closing quote (s starts after
// the opening one), resolving the escapes \\, \" and \n.
func parseQuoted(s string) (val, rest string, err error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			i++
			if i >= len(s) {
				return "", "", webcheck.ErrFormat
			}
			switch s[i] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			default:
				return "", "", webcheck.ErrFormat
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", webcheck.ErrFormat
}

// validLabelName reports whether s is a Prometheus label name.
func validLabelName(s string) bool {
	if s == "" || strings.Contains(s, ":") {
		return false
	}
	return validName(s)
}
