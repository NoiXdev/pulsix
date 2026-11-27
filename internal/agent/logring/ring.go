// Package logring keeps the agent's last log lines in memory for its local
// status page (backlog 3.132). The binary tees its logger into a Ring; the
// page reads Lines. Before a line is stored, every current secret value is
// replaced (SetRedact), and Lines redacts again on the way out, so a line
// written before the secrets were first read is covered too. The ring
// never fails a write: the log's real destination must not suffer for it.
package logring

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
)

// MaxLineBytes is the longest line the ring stores; a longer one is cut
// and ends in truncated.
const MaxLineBytes = 4096

const truncated = " …[cut]"

// Ring holds the last n complete lines written to it. It is safe for
// concurrent use.
type Ring struct {
	redact atomic.Pointer[func(string) string]

	mu      sync.Mutex
	lines   []string // circular, len == cap once full
	next    int
	full    bool
	pending []byte // the start of a line without its end yet
	cut     bool   // pending lost bytes over MaxLineBytes
}

// New returns a ring of n lines.
func New(n int) *Ring {
	if n < 1 {
		n = 1
	}
	return &Ring{lines: make([]string, n)}
}

// SetRedact installs the function that replaces secret values. It must be
// safe to call from any goroutine and must not log.
func (r *Ring) SetRedact(fn func(string) string) { r.redact.Store(&fn) }

func (r *Ring) redacted(s string) string {
	if fn := r.redact.Load(); fn != nil {
		return (*fn)(s)
	}
	return s
}

// Write stores every complete line of p and holds a trailing partial one
// until its end arrives. It always reports len(p) written.
func (r *Ring) Write(p []byte) (int, error) {
	n := len(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			r.hold(p)
			break
		}
		r.hold(p[:i])
		r.store()
		p = p[i+1:]
	}
	return n, nil
}

// hold appends b to the pending line, keeping a little over MaxLineBytes
// so redaction can still see a value that straddles the cut.
func (r *Ring) hold(b []byte) {
	room := 2*MaxLineBytes - len(r.pending)
	if room <= 0 {
		r.cut = r.cut || len(b) > 0
		return
	}
	if len(b) > room {
		b, r.cut = b[:room], true
	}
	r.pending = append(r.pending, b...)
}

// store redacts, cuts and stores the pending line; r.mu is held.
func (r *Ring) store() {
	line := r.redacted(string(bytes.TrimSuffix(r.pending, []byte("\r"))))
	if len(line) > MaxLineBytes || r.cut {
		if len(line) > MaxLineBytes {
			line = strings.ToValidUTF8(line[:MaxLineBytes], "")
		}
		line += truncated
	}
	r.pending, r.cut = r.pending[:0], false
	r.lines[r.next] = line
	r.next = (r.next + 1) % len(r.lines)
	r.full = r.full || r.next == 0
}

// Lines returns the stored lines, oldest first, redacted again with the
// current redactor.
func (r *Ring) Lines() []string {
	r.mu.Lock()
	var out []string
	if r.full {
		out = append(out, r.lines[r.next:]...)
	}
	out = append(out, r.lines[:r.next]...)
	r.mu.Unlock()
	for i, l := range out {
		out[i] = r.redacted(l)
	}
	return out
}
