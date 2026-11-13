package checks

import (
	"log/slog"
	"sync"
	"time"
)

// advisoryRepeat is how long an unchanged advisory error stays quiet before
// it is logged again.
const advisoryRepeat = time.Hour

// advisoryLog logs the advisory errors plugins return next to their
// observations (Plugin.Collect), once per change and at most hourly while
// the text stays the same, with state per check key. A nil error clears the
// key's state without a line of its own. Only callers that know the plugin
// is not a SecretPlugin may use it: the text is logged as it is.
type advisoryLog struct {
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	state map[string]advisoryState
}

type advisoryState struct {
	text string
	at   time.Time
}

func newAdvisoryLog(log *slog.Logger, now func() time.Time) *advisoryLog {
	return &advisoryLog{log: log, now: now, state: make(map[string]advisoryState)}
}

// note records the outcome of one round of check key: err is the plugin's
// advisory error or nil.
func (a *advisoryLog) note(key, plugin string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		delete(a.state, key)
		return
	}
	text, now := err.Error(), a.now()
	if prev, ok := a.state[key]; ok && prev.text == text && now.Sub(prev.at) < advisoryRepeat {
		return
	}
	a.state[key] = advisoryState{text: text, at: now}
	a.log.Warn("check advisory", "check", key, "plugin", plugin, "advisory", text)
}

// forget drops key's state, for a check that is retired.
func (a *advisoryLog) forget(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.state, key)
}
