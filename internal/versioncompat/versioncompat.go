// Package versioncompat classifies a worker's or agent's build version
// against the manager's. The rule (architecture spec 2026-09-10): the
// manager accepts the same and the previous minor version. A component
// outside that is shown as such, never refused: rejecting older components
// would cut a fleet off in the middle of an upgrade.
package versioncompat

import (
	"strconv"
	"strings"
	"sync"
)

// Status is how a component's version relates to the manager's. The string
// values are the API's version_status.
type Status string

const (
	// Current is the manager's own minor version; the patch is ignored.
	Current Status = "current"
	// PreviousMinor is exactly one minor version below the manager's:
	// still supported, worth updating at the next opportunity.
	PreviousMinor Status = "previous_minor"
	// UpdateRequired is more than one minor below the manager's, or an
	// older major version.
	UpdateRequired Status = "update_required"
	// Newer is above the manager's minor version (or major): the manager
	// was rolled back or is not yet updated.
	Newer Status = "newer"
	// Unknown means no verdict: either side is empty, a development build
	// ("dev"), the embedded worker, or not a MAJOR.MINOR.PATCH version.
	Unknown Status = "unknown"
)

// NeedsAttention reports whether the status is one an operator has to act
// on: UpdateRequired or Newer.
func (s Status) NeedsAttention() bool { return s == UpdateRequired || s == Newer }

// Of classifies component against manager. Both are "vMAJOR.MINOR.PATCH"
// with or without the leading "v"; a pre-release or build suffix
// ("-rc.1", "+abc", "-12-gabc1234") is ignored, as is the patch number.
func Of(manager, component string) Status {
	m, ok := parse(manager)
	if !ok {
		return Unknown
	}
	c, ok := parse(component)
	if !ok {
		return Unknown
	}
	switch {
	case c.major < m.major:
		return UpdateRequired
	case c.major > m.major:
		return Newer
	case c.minor == m.minor:
		return Current
	case c.minor == m.minor-1:
		return PreviousMinor
	case c.minor < m.minor:
		return UpdateRequired
	default:
		return Newer
	}
}

type version struct{ major, minor int }

// parse reads MAJOR.MINOR.PATCH; anything else (including "dev" and "") is
// not a version.
func parse(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var n [3]int
	for i, p := range parts {
		// Digits only: Atoi alone would accept "+4" and "-4".
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return version{}, false
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		n[i] = v
	}
	return version{major: n[0], minor: n[1]}, true
}

// Warner remembers which components the process has already warned about,
// so the log carries one line per component, not one per heartbeat. The
// zero value is ready to use.
type Warner struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// Observe classifies version against manager and reports whether this is
// the first time key (a component, e.g. "worker/<id>") was seen with a
// status that needs attention in this process; the caller logs then.
func (w *Warner) Observe(key, manager, version string) (Status, bool) {
	st := Of(manager, version)
	if !st.NeedsAttention() {
		return st, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.seen[key]; dup {
		return st, false
	}
	if w.seen == nil {
		w.seen = make(map[string]struct{})
	}
	w.seen[key] = struct{}{}
	return st, true
}
