package checks

import (
	"log/slog"
	"sync"
	"time"
)

// advisoryRepeat is how long an unchanged advisory error stays quiet before
// it is logged again.
const advisoryRepeat = time.Hour

// advisoryLog logs the advisory errors one check's plugin returns next to
// its observations (Plugin.Collect), once per change and at most hourly
// while the text stays the same. A nil error clears the state without a line
// of its own. It belongs to one scheduledCheck instance, so a check that is
// retired or restarted takes its state with it and a goroutine that
// finishes late can only touch the state of its own instance. Only callers
// that know the plugin is not a SecretPlugin may use it: the text is logged
// as it is.
type advisoryLog struct {
	log    *slog.Logger
	now    func() time.Time
	key    string
	plugin string

	mu   sync.Mutex
	have bool
	text string
	at   time.Time
}

func newAdvisoryLog(log *slog.Logger, now func() time.Time, key, plugin string) *advisoryLog {
	return &advisoryLog{log: log, now: now, key: key, plugin: plugin}
}

// note records the outcome of one round: err is the plugin's advisory error
// or nil.
func (a *advisoryLog) note(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.have = false
		return
	}
	text, now := err.Error(), a.now()
	if a.have && a.text == text && now.Sub(a.at) < advisoryRepeat {
		return
	}
	a.have, a.text, a.at = true, text, now
	a.log.Warn("check advisory", "check", a.key, "plugin", a.plugin, "advisory", text)
}
