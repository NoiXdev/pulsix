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

// parseStatus reads the stub_status page strictly: four non-empty lines in
// the module's fixed layout, every number a base-10 uint64. Whitespace
// between and around the tokens is free; anything else is a format error,
// as is handled above accepts (the server never handles what it did not
// accept).
func parseStatus(body []byte) (status, error) {
	var lines [][]string
	for _, l := range strings.Split(string(body), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			lines = append(lines, f)
		}
	}
	if len(lines) != 4 {
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
		n, err := strconv.ParseUint(f.src, 10, 64)
		if err != nil {
			return status{}, errFormat
		}
		*f.dst = n
	}
	if s.handled > s.accepts {
		return status{}, errFormat
	}
	return s, nil
}
