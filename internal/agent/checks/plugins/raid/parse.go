package raid

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// State labels of raid.state.
const (
	stateClean      = "clean"
	stateDegraded   = "degraded"
	stateResyncing  = "resyncing"
	stateRecovering = "recovering"
	stateChecking   = "checking"
	stateReshaping  = "reshaping"
	stateInactive   = "inactive"
)

// array is one md device as /proc/mdstat describes it.
type array struct {
	name    string
	state   string
	total   int
	active  int
	failed  int
	syncPct float64
	hasPct  bool
}

var (
	// "md0 : active raid1 sdb1[1] sda1[0]" — the header line of an array.
	headerRe = regexp.MustCompile(`^(\S+) : (.*)$`)
	// A member such as sda1[0], sdb1[1](F) or sdc1[2](S).
	memberRe = regexp.MustCompile(`^[^\s\[]+\[\d+\]((?:\([A-Z]\))*)$`)
	// "[4/3]": configured devices / working devices.
	countsRe = regexp.MustCompile(`\[(\d+)/(\d+)\]`)
	// "[UU_U]": per-slot health.
	slotsRe = regexp.MustCompile(`\[[U_]+\]`)
	// "recovery = 12.3%".
	syncPctRe = regexp.MustCompile(`\b(recovery|resync|check|reshape|repair)\s*=\s*(\d+(?:\.\d+)?)%`)
	// "resync=PENDING" / "resync=DELAYED" — queued, no percentage yet.
	syncQueuedRe = regexp.MustCompile(`\b(recovery|resync|check|reshape|repair)\s*=\s*(?:PENDING|DELAYED)\b`)
)

var syncStates = map[string]string{
	"recovery": stateRecovering,
	"resync":   stateResyncing,
	"check":    stateChecking,
	"reshape":  stateReshaping,
	"repair":   stateChecking,
}

// parseMdstat reads /proc/mdstat. Lines it does not recognise are ignored,
// so a kernel adding fields cannot break the check.
func parseMdstat(data []byte) []array {
	var (
		out []array
		cur *block
	)
	flush := func() {
		if cur != nil {
			out = append(out, cur.finish())
			cur = nil
		}
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if m := headerRe.FindStringSubmatch(line); m != nil && m[1] != "Personalities" {
			flush()
			cur = newBlock(m[1], m[2])
			continue
		}
		if strings.HasPrefix(line, "unused devices:") || strings.HasPrefix(line, "Personalities") {
			flush()
			continue
		}
		if cur != nil {
			cur.addLine(line)
		}
	}
	flush()
	return out
}

// block accumulates the lines of one array.
type block struct {
	name       string
	inactive   bool
	members    int
	failed     int
	spare      int
	haveCounts bool
	total      int
	working    int
	slotsLost  bool
	syncState  string
	pct        float64
	hasPct     bool
}

func newBlock(name, rest string) *block {
	b := &block{name: name}
	for i, f := range strings.Fields(rest) {
		if i == 0 && f == "inactive" {
			b.inactive = true
		}
		if m := memberRe.FindStringSubmatch(f); m != nil {
			b.members++
			if strings.Contains(m[1], "(F)") {
				b.failed++
			}
			if strings.Contains(m[1], "(S)") {
				b.spare++
			}
		}
	}
	return b
}

func (b *block) addLine(line string) {
	if m := countsRe.FindStringSubmatch(line); m != nil && !b.haveCounts {
		n, err1 := strconv.Atoi(m[1])
		w, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil {
			b.haveCounts, b.total, b.working = true, n, w
		}
	}
	if slotsRe.MatchString(line) && strings.Contains(slotsRe.FindString(line), "_") {
		b.slotsLost = true
	}
	if m := syncPctRe.FindStringSubmatch(line); m != nil {
		if v, err := strconv.ParseFloat(m[2], 64); err == nil {
			b.syncState, b.pct, b.hasPct = syncStates[m[1]], v, true
		}
	} else if m := syncQueuedRe.FindStringSubmatch(line); m != nil {
		b.syncState = syncStates[m[1]]
	}
}

func (b *block) finish() array {
	a := array{name: b.name, failed: b.failed}
	if b.haveCounts {
		a.total, a.active = b.total, b.working
	} else {
		a.total = b.members
		a.active = b.members - b.failed - b.spare
	}
	switch {
	case b.inactive:
		a.state = stateInactive
	case b.syncState != "":
		a.state = b.syncState
		a.syncPct, a.hasPct = b.pct, b.hasPct
	case b.slotsLost || (b.haveCounts && b.working < b.total) || b.failed > 0:
		a.state = stateDegraded
	default:
		a.state = stateClean
	}
	return a
}
