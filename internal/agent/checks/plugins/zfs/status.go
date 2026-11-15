package zfs

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// zpoolStatusArgs is `zpool status` for every pool, with exact error
// counters (-p).
var zpoolStatusArgs = []string{"status", "-p"}

// poolStatus is what zpool status says about one pool.
type poolStatus struct {
	scan                                    scanState
	readErrors, writeErrors, checksumErrors number
	devicesUnhealthy, dataErrors            number
}

// treeRow is one device row of a pool's configuration tree.
type treeRow struct {
	indent int    // leading spaces: two per level
	group  string // "" for the pool's own tree, else logs, cache, …
	state  string
}

// groups are the configuration tree's top-level sections after the
// pool's own tree; each is a row of its name alone.
var groups = map[string]bool{"logs": true, "cache": true, "spares": true, "special": true, "dedup": true}

// states are the device states zpool prints, the spares' AVAIL and INUSE
// included; the row's state is the first of them after its name, which
// may hold a space.
var states = map[string]bool{
	"ONLINE": true, "DEGRADED": true, "FAULTED": true, "OFFLINE": true, "UNAVAIL": true,
	"REMOVED": true, "SPLIT": true, "UNKNOWN": true, "SUSPENDED": true, "AVAIL": true, "INUSE": true,
}

var dataErrorsLine = regexp.MustCompile(`^(\d+) data errors?, use '-v' for a list$`)

// counterForm is an error counter: exact with -p, abbreviated by older
// releases (1.2K), as zpool's nicenum does, in powers of 1024.
var counterForm = regexp.MustCompile(`^(\d+(?:\.\d+)?)([KMGTPE]?)$`)

// poolParse is one pool's paragraph while it is read.
type poolParse struct {
	name       string
	section    string
	scans      [][]string // one per scan: header
	hasConfig  bool
	rows       []treeRow
	sums       [3]float64
	sumErrs    [3]error
	treeErr    error
	errorsText string
	hasErrors  bool
	group      string
	sawHeader  bool
}

// parseStatus reads zpool status's output into one poolStatus per pool.
// A pool named twice keeps its first paragraph.
func parseStatus(out []byte, loc *time.Location) map[string]poolStatus {
	pools := map[string]poolStatus{}
	var cur *poolParse
	finish := func() {
		if cur != nil {
			if _, dup := pools[cur.name]; !dup {
				pools[cur.name] = cur.result(loc)
			}
		}
	}
	for raw := range bytes.Lines(out) {
		line := strings.TrimRight(string(raw), "\r\n")
		if key, rest, ok := headerLine(line); ok {
			if key == "pool" {
				finish()
				cur = &poolParse{name: rest}
				continue
			}
			if cur == nil {
				continue
			}
			cur.section = key
			switch key {
			case "scan", "scrub": // "scrub": OpenZFS 2.2's error scrub
				cur.section = "scan"
				cur.scans = append(cur.scans, []string{rest})
			case "config":
				cur.hasConfig = true
			case "errors":
				cur.errorsText, cur.hasErrors = strings.TrimSpace(rest), true
			}
			continue
		}
		if cur == nil || strings.TrimSpace(line) == "" {
			continue
		}
		switch cur.section {
		case "scan":
			cur.scans[len(cur.scans)-1] = append(cur.scans[len(cur.scans)-1], line)
		case "config":
			if strings.HasPrefix(line, "\t") {
				cur.row(line[1:])
			}
		}
	}
	finish()
	return pools
}

// headerLine splits a paragraph's "  key: value" line: a lower-case key,
// right-aligned with spaces (never a tab, which starts a continuation or a
// tree row), then a colon and a space or the line's end.
func headerLine(line string) (key, rest string, ok bool) {
	s := strings.TrimLeft(line, " ")
	key, rest, found := strings.Cut(s, ":")
	if !found || key == "" || strings.TrimLeft(key, "abcdefghijklmnopqrstuvwxyz") != "" {
		return "", "", false
	}
	if rest == "" {
		return key, "", true
	}
	if rest[0] != ' ' {
		return "", "", false
	}
	return key, rest[1:], true
}

// row reads one line of the configuration tree (without its leading tab).
func (p *poolParse) row(s string) {
	indent := len(s) - len(strings.TrimLeft(s, " "))
	f := strings.Fields(s)
	if len(f) == 0 {
		return
	}
	if !p.sawHeader && len(f) >= 2 && f[0] == "NAME" && f[1] == "STATE" {
		p.sawHeader = true
		return
	}
	if len(f) == 1 && indent == 0 && groups[f[0]] {
		p.group = f[0]
		return
	}
	i := 1
	for i < len(f) && !states[f[i]] {
		i++
	}
	if i == len(f) {
		if p.treeErr == nil {
			p.treeErr = fmt.Errorf("unrecognized configuration row %q", cutText(strings.TrimSpace(s), maxQuote))
		}
		return
	}
	p.rows = append(p.rows, treeRow{indent: indent, group: p.group, state: f[i]})
	if p.group == "spares" {
		return // a spare's row carries no counters
	}
	cols := [3]string{"READ", "WRITE", "CKSUM"}
	for c := range cols {
		if p.sumErrs[c] != nil {
			continue
		}
		if i+1+c >= len(f) {
			p.sumErrs[c] = fmt.Errorf("configuration row %q has no %s counter", cutText(strings.TrimSpace(s), maxQuote), cols[c])
			continue
		}
		v, err := parseCounter(f[i+1+c])
		if err != nil {
			p.sumErrs[c] = fmt.Errorf("%s counter: %w", cols[c], err)
			continue
		}
		p.sums[c] += v
	}
}

// parseCounter reads one error counter.
func parseCounter(s string) (float64, error) {
	m := counterForm.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a number", cutText(s, maxQuote))
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", cutText(s, maxQuote))
	}
	if m[2] == "" {
		return v, nil
	}
	return math.Round(v * math.Pow(1024, float64(strings.Index(" KMGTPE", m[2])))), nil
}

var (
	errNoTree   = errors.New("no configuration tree in zpool status")
	errNoErrors = errors.New("no errors line in zpool status")
)

// result turns the paragraph into the pool's values.
func (p *poolParse) result(loc *time.Location) poolStatus {
	st := poolStatus{scan: parseScan(p.scans, loc)}

	treeErr := p.treeErr
	if treeErr == nil && (!p.hasConfig || len(p.rows) == 0) {
		treeErr = errNoTree
	}
	sums := [3]*number{&st.readErrors, &st.writeErrors, &st.checksumErrors}
	for c, n := range sums {
		switch {
		case treeErr != nil:
			*n = number{err: treeErr}
		case p.sumErrs[c] != nil:
			*n = number{err: p.sumErrs[c]}
		default:
			*n = value(p.sums[c])
		}
	}
	if treeErr != nil {
		st.devicesUnhealthy = number{err: treeErr}
	} else {
		st.devicesUnhealthy = value(float64(p.unhealthyLeaves()))
	}

	st.dataErrors = dataErrors(p.errorsText, p.hasErrors)
	return st
}

// unhealthyLeaves counts the leaves — rows with no deeper row beneath them
// in their group — that are not ONLINE, the spares group aside: a spare
// that stands in appears in the vdev it replaces and counts there.
func (p *poolParse) unhealthyLeaves() int {
	n := 0
	for k, r := range p.rows {
		if r.group == "spares" || r.state == "ONLINE" {
			continue
		}
		if k+1 < len(p.rows) && p.rows[k+1].group == r.group && p.rows[k+1].indent > r.indent {
			continue // not a leaf
		}
		n++
	}
	return n
}

// dataErrors reads the errors line.
func dataErrors(text string, present bool) number {
	if !present {
		return number{err: errNoErrors}
	}
	if text == "No known data errors" {
		return value(0)
	}
	if m := dataErrorsLine.FindStringSubmatch(text); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			return value(v)
		}
	}
	return number{err: fmt.Errorf("zpool status reports %q", cutText(text, maxQuote))}
}
