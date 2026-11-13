package apache

import (
	"errors"
	"strconv"
	"strings"
)

// status is what the mod_status ?auto page reports. extended is true when
// the page carries Total Accesses and Total kBytes (ExtendedStatus On).
type status struct {
	busy, idle       uint64
	accesses, kbytes uint64
	extended         bool
}

var errFormat = errors.New("unexpected response format")

const (
	// maxPage bounds what parseStatus looks at. A real page is a few KB
	// besides the Scoreboard, which is ServerLimit x ThreadLimit characters
	// (16 x 64 by default, tens of KB on a big configuration); 1 MiB leaves
	// room for that and stays far below the 4 MiB the fetch allows. There is
	// no per-line bound: the Scoreboard is never parsed, the page bound
	// limits the work, and a valid big configuration must read.
	maxPage = 1 << 20
)

// parseStatus reads the page by key: lines of "Key: value", unknown keys
// and lines without a colon (the host name on top) are ignored, the first
// occurrence of a key wins. BusyWorkers and IdleWorkers are required;
// Total Accesses and Total kBytes are optional as a pair (ExtendedStatus
// Off omits them) but must be base-10 uint64 when present. Anything else is
// a format error.
func parseStatus(body []byte) (status, error) {
	if len(body) > maxPage {
		return status{}, errFormat
	}
	var s status
	var haveBusy, haveIdle, haveAcc, haveKB bool
	rest := string(body)
	for rest != "" {
		line, tail, _ := strings.Cut(rest, "\n")
		rest = tail
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var dst *uint64
		var seen *bool
		switch key {
		case "BusyWorkers":
			dst, seen = &s.busy, &haveBusy
		case "IdleWorkers":
			dst, seen = &s.idle, &haveIdle
		case "Total Accesses":
			dst, seen = &s.accesses, &haveAcc
		case "Total kBytes":
			dst, seen = &s.kbytes, &haveKB
		default:
			continue
		}
		if *seen {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return status{}, errFormat
		}
		*dst, *seen = v, true
	}
	if !haveBusy || !haveIdle {
		return status{}, errFormat
	}
	s.extended = haveAcc && haveKB
	if !s.extended {
		s.accesses, s.kbytes = 0, 0
	}
	return s, nil
}
