package caddy

import (
	"errors"
	"math"
	"strconv"
	"strings"
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
	// errors5xx is the counter of responses with a code of 500-599, summed
	// over the codes of each (server, handler) pair before the largest
	// handler is taken.
	errors5xx uint64
	have5xx   bool
}

var errFormat = errors.New("unexpected response format")

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
	inflight map[key]float64
	requests map[key]uint64
	errs     map[key]uint64
	haveDur  bool
}

// parseMetrics reads the Prometheus text exposition format line by line:
// comments and blank lines are skipped, every other line must be
// "name[{labels}] value [timestamp]" with a valid metric name. Only the
// three families the plugin reports are parsed beyond the name; a broken
// line of one of them, or a line that is no sample at all (an HTML page),
// is a format error. A sample whose value is NaN, infinite or negative, or
// a counter above 2^53, is ignored.
func parseMetrics(body []byte) (metrics, error) {
	a := aggregate{
		inflight: map[key]float64{},
		requests: map[key]uint64{},
		errs:     map[key]uint64{},
	}
	samples := 0
	rest := string(body)
	for rest != "" {
		line, tail, _ := strings.Cut(rest, "\n")
		rest = tail
		if len(line) > maxLine {
			return metrics{}, errFormat
		}
		line = strings.TrimLeft(strings.TrimRight(line, " \t\r"), " \t")
		if line == "" || line[0] == '#' {
			continue
		}
		end := strings.IndexAny(line, " \t{")
		if end < 0 || !validName(line[:end]) {
			return metrics{}, errFormat
		}
		samples++
		name := line[:end]
		if name != famInflight && name != famRequests && name != famDuration {
			continue
		}
		if err := a.add(name, line[end:]); err != nil {
			return metrics{}, err
		}
	}
	if samples == 0 {
		return metrics{}, errFormat
	}
	return a.fold(), nil
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
	fields := strings.Fields(rest)
	if len(fields) < 1 || len(fields) > 2 {
		return errFormat
	}
	if len(fields) == 2 {
		if _, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
			return errFormat
		}
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return errFormat
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	k := key{server, handler}
	if name == famInflight {
		a.inflight[k] += v
		return nil
	}
	if v > maxCounter {
		return nil
	}
	n := uint64(v)
	switch name {
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
	m := metrics{haveInflight: len(a.inflight) > 0, haveRequests: len(a.requests) > 0, have5xx: a.haveDur}
	perServer := map[string]float64{}
	for k, v := range a.inflight {
		perServer[k.server] = math.Max(perServer[k.server], v)
	}
	for _, v := range perServer {
		m.inflight += v
	}
	m.requests = sumOfLargest(a.requests)
	m.errors5xx = sumOfLargest(a.errs)
	return m
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
			return "", "", "", "", errFormat
		}
		if s[0] == '}' {
			return server, handler, code, s[1:], nil
		}
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			return "", "", "", "", errFormat
		}
		label := strings.TrimSpace(s[:eq])
		s = strings.TrimLeft(s[eq+1:], " \t")
		if s == "" || s[0] != '"' {
			return "", "", "", "", errFormat
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
			return "", "", "", "", errFormat
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
				return "", "", errFormat
			}
			switch s[i] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			default:
				return "", "", errFormat
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", errFormat
}
