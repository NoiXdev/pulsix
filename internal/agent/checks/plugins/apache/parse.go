package apache

import (
	"strconv"
	"strings"

	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
)

// status is what the mod_status ?auto page reports. extended is true when
// the page carries Total Accesses and Total kBytes (ExtendedStatus On).
// slots is the Scoreboard line's length, the server's worker limit (backlog
// 3.11b); slotsReason is why it is not known, "" when it is.
type status struct {
	busy, idle       uint64
	accesses, kbytes uint64
	extended         bool
	slots            uint64
	slotsReason      string
}

// reasonNoScoreboard is the two limit metrics' text for a page without a
// Scoreboard line.
const reasonNoScoreboard = "not reported by the server (no Scoreboard)"

// scoreboardStates are the characters mod_status prints per worker slot:
// open slot, starting, ready, reading, writing, keepalive, DNS, closing,
// logging, gracefully finishing, idle cleanup.
const scoreboardStates = "._SRWKDCLGI"

const (
	// maxPage bounds what parseStatus looks at. A real page is a few KB
	// besides the Scoreboard, which has one character per slot of the
	// configured processes plus every slot beyond them that is not dead, so
	// at most ServerLimit x ThreadLimit characters (tens of KB on a big
	// configuration); 1 MiB leaves room for that and stays far below the
	// 4 MiB the fetch allows. There is no per-line bound: the Scoreboard is
	// read for its length only, the page bound limits the work, and a valid
	// big configuration must read.
	maxPage = 1 << 20
)

// parseStatus reads the page by key: lines of "Key: value", unknown keys
// and lines without a colon (the host name on top) are ignored, the first
// occurrence of a key wins. BusyWorkers and IdleWorkers are required;
// Total Accesses and Total kBytes are optional as a pair (ExtendedStatus
// Off omits them) but must be base-10 uint64 when present. Anything else is
// a format error. The Scoreboard line only sets slots or slotsReason: a
// page without one, or with one that is not a scoreboard, still reads.
func parseStatus(body []byte) (status, error) {
	if len(body) > maxPage {
		return status{}, webcheck.ErrFormat
	}
	s := status{slotsReason: reasonNoScoreboard}
	var haveBusy, haveIdle, haveAcc, haveKB, haveBoard bool
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
		case "Scoreboard":
			if !haveBoard {
				haveBoard = true
				s.slots, s.slotsReason = scoreboardSlots(val)
			}
			continue
		default:
			continue
		}
		if *seen {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return status{}, webcheck.ErrFormat
		}
		*dst, *seen = v, true
	}
	if !haveBusy || !haveIdle {
		return status{}, webcheck.ErrFormat
	}
	s.extended = haveAcc && haveKB
	if !s.extended {
		s.accesses, s.kbytes = 0, 0
	}
	return s, nil
}

// scoreboardSlots is the number of worker slots on a Scoreboard line's
// value: every character one of scoreboardStates, at least one.
func scoreboardSlots(val string) (uint64, string) {
	board := strings.TrimSpace(val)
	if board == "" || strings.Trim(board, scoreboardStates) != "" {
		return 0, webcheck.ErrFormat.Error()
	}
	return uint64(len(board)), ""
}
