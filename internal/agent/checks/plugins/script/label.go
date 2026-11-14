package script

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// noOutput is script.output for an empty message.
const noOutput = "(no output)"

// maxValues bounds the script.value observations of one command.
const maxValues = 64

// cleanMessage turns a message into a script.output label the manager
// accepts: invalid UTF-8 replaced, control characters and line or
// paragraph separators removed, trimmed, cut to checks.MaxLabelLen bytes
// on a rune boundary; empty becomes noOutput.
func cleanMessage(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > checks.MaxLabelLen {
		n := checks.MaxLabelLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = strings.TrimSpace(s[:n])
	}
	if s == "" {
		return noOutput
	}
	return s
}

// itemValue is one script.value observation: its item and value.
type itemValue struct {
	item  string
	value float64
}

// valueSet collects one command's values in output order: items
// "<command>:<label>", at most maxValues, the first value of a repeated
// label, and none whose label is empty or whose item the manager would
// refuse (checks.ReportableItem).
type valueSet struct {
	command string
	values  []itemValue
	seen    map[string]bool
}

func newValueSet(command string) *valueSet {
	return &valueSet{command: command, seen: map[string]bool{}}
}

// add records label's value unless the set is full or the label is
// empty, repeated or unreportable.
func (vs *valueSet) add(label string, v float64) {
	if label == "" || vs.full() {
		return
	}
	item := vs.command + ":" + label
	if vs.seen[item] || !checks.ReportableItem(item) {
		return
	}
	vs.seen[item] = true
	vs.values = append(vs.values, itemValue{item: item, value: v})
}

// full reports whether the set holds maxValues values.
func (vs *valueSet) full() bool { return len(vs.values) >= maxValues }
