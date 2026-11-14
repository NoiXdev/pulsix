package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/agent/secrets"
)

// maxSecretsStateLen bounds the secrets file's state as the heartbeat
// carries it: the error text names the file's path, which the operator
// chooses (--config).
const maxSecretsStateLen = 2048

// newSecretsWatcher watches the agent's configuration file (backlog 3.5)
// under the same protection rule as its env file, through the loop's own
// protector, clock and logger. It has no goroutine: the heartbeat and the
// scheduler's secret and command lookups drive it, and it reads the file
// at most once a minute.
func (l *Loop) newSecretsWatcher() *secrets.Watcher {
	return secrets.NewWatcher(l.cfg.SecretsPath,
		func(path string) error { return checkConfigFile(l.protect, path) },
		secrets.WithClock(l.now), secrets.WithLogger(l.log))
}

// secretsSource is the scheduler's checks.Secrets: the watcher's current
// set, and why it has no secrets when the file is missing or refused.
type secretsSource struct{ w *secrets.Watcher }

func (s secretsSource) Secret(name string) (map[string]string, bool, string) {
	set, state := s.w.Current()
	if fields, ok := set.Fields(name); ok {
		return fields, true, ""
	}
	return nil, false, fileProblem(s.w, state)
}

// commandsSource is the scheduler's checks.Commands: the watcher's current
// set, and why it has no commands when the file is missing or refused.
type commandsSource struct{ w *secrets.Watcher }

func (s commandsSource) Command(name string) ([]string, bool, string) {
	set, state := s.w.Current()
	if argv, ok := set.Command(name); ok {
		return argv, true, ""
	}
	return nil, false, fileProblem(s.w, state)
}

// fileProblem says why the watched file defines nothing in state: empty
// when the file is fine, so only the name looked up is missing.
func fileProblem(w *secrets.Watcher, state secrets.State) string {
	switch state {
	case secrets.StateOK:
		return ""
	case secrets.StateAbsent:
		return reportable("the file " + w.Path() + " does not exist")
	default:
		return reportable("the file is not usable: " + string(state))
	}
}

// reportedSecretsState is the state as the heartbeat carries it.
func reportedSecretsState(state secrets.State) string { return reportable(string(state)) }

// reportable makes s safe for a proto3 string (valid UTF-8, or gRPC
// refuses the whole message) and cuts it to maxSecretsStateLen on a rune
// boundary.
func reportable(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxSecretsStateLen {
		return s
	}
	n := maxSecretsStateLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
