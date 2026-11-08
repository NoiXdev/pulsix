package requirements

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/worker/urlcred"
)

// maxCommandLines is how many lines of one galaxy command reach the log
// (Ruling 6); the rest is counted.
const maxCommandLines = 500

// maxStderrTail is how much of a failed command's stderr its error keeps.
const maxStderrTail = 4 << 10

// commandLines passes one command's lines to out: credentials removed, at
// most maxCommandLines, then done reports how many were left out. line is
// safe for concurrent use (stdout and stderr may arrive on two goroutines).
type commandLines struct {
	out func(string)

	mu      sync.Mutex
	sent    int
	dropped int
}

func (c *commandLines) line(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent >= maxCommandLines {
		c.dropped++
		return
	}
	c.sent++
	c.out(urlcred.Redact(s))
}

// done sends "… N more lines" when lines were left out.
func (c *commandLines) done() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped > 0 {
		c.out(fmt.Sprintf("… %d more lines", c.dropped))
	}
}

// stderrTail is the end of a failed command's stderr as its error shows it:
// credentials removed first, trailing white space trimmed, then the last
// maxStderrTail bytes, starting at a whole character.
func stderrTail(stderr string) string {
	s := strings.TrimRight(urlcred.Redact(stderr), " \t\r\n")
	if len(s) <= maxStderrTail {
		return s
	}
	s = s[len(s)-maxStderrTail:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}
