package nginx

import (
	"errors"
	"strconv"
	"strings"
)

// status is what stub_status reports.
type status struct {
	active, accepts, handled, requests uint64
	reading, writing, waiting          uint64
}

var errFormat = errors.New("unexpected response format")

// maxPage bounds what parseStatus looks at: a real page is about 120 bytes.
const maxPage = 4 << 10

// parseStatus reads the stub_status page strictly: four non-empty lines in
// the module's fixed layout, every number a base-10 uint64. Whitespace
// between and around the tokens is free; anything else is a format error.
// handled above accepts is not one: nginx reads its counters without a lock
// while workers keep counting, so a busy server can print it (nothing was
// dropped then, see dropped).
func parseStatus(body []byte) (status, error) {
	if len(body) > maxPage {
		return status{}, errFormat
	}
	rest := string(body)
	var lines [4][]string
	n := 0
	for rest != "" {
		var l string
		l, rest, _ = strings.Cut(rest, "\n")
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if n == len(lines) {
			return status{}, errFormat
		}
		lines[n] = f
		n++
	}
	if n != len(lines) {
		return status{}, errFormat
	}
	var s status
	a, h, c, r := lines[0], lines[1], lines[2], lines[3]
	if len(a) != 3 || a[0] != "Active" || a[1] != "connections:" ||
		strings.Join(h, " ") != "server accepts handled requests" ||
		len(c) != 3 || len(r) != 6 ||
		r[0] != "Reading:" || r[2] != "Writing:" || r[4] != "Waiting:" {
		return status{}, errFormat
	}
	for _, f := range []struct {
		dst *uint64
		src string
	}{
		{&s.active, a[2]}, {&s.accepts, c[0]}, {&s.handled, c[1]}, {&s.requests, c[2]},
		{&s.reading, r[1]}, {&s.writing, r[3]}, {&s.waiting, r[5]},
	} {
		v, err := strconv.ParseUint(f.src, 10, 64)
		if err != nil {
			return status{}, errFormat
		}
		*f.dst = v
	}
	return s, nil
}

// dropped is accepts - handled, or 0 when the racing read shows handled
// above accepts.
func (s status) dropped() uint64 {
	if s.handled > s.accepts {
		return 0
	}
	return s.accepts - s.handled
}
