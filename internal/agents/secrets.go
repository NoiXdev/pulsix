package agents

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// Bounds on what one heartbeat may store about the agent's local secrets
// file (backlog 3.5). The agent keeps to them itself (internal/agent's
// secrets file); the manager enforces them again because the stream is the
// agent's to fill — an untrusted or broken agent must not be able to store
// more, or other, than a list of names and one line of state.
const (
	// MaxSecretNames is how many names are kept: the agent loads at most
	// as many secrets.
	MaxSecretNames = 256
	// MaxSecretsStateBytes bounds the state text, cut on a rune boundary.
	MaxSecretsStateBytes = 2048
)

// SecretsReport is what an agent last said about its local secrets file:
// the names of the secrets it loaded (never a value) and the file's state
// ("absent", "ok" or the error that made it drop every secret).
//
// Known is false when there is no report: the agent is older than the
// file, or it has not sent a heartbeat since it enrolled (a certificate
// renewal keeps the report). Names is then nil and State empty; when
// Known, Names is non-nil (possibly empty) and sorted.
type SecretsReport struct {
	Known bool
	Names []string
	State string
}

// secretsReportOf bounds one heartbeat's raw fields into a report.
//
// proto3 cannot tell an absent repeated field from an empty one, so the
// state decides whether the agent reports at all: every agent that reads
// agent.yaml sends a non-empty state ("absent" when there is no file), an
// older one sends none. An empty state is therefore "unknown", whatever
// names ride along.
//
// Names that fail checkplugins' secretName format (the agent's own rule
// for a secret's name in agent.yaml) are dropped, duplicates
// kept once, the rest sorted and capped at MaxSecretNames. The state is
// made valid UTF-8 (invalid bytes become U+FFFD), its control characters
// become spaces (Postgres text refuses NUL, and the GUI shows one line),
// and it is cut to MaxSecretsStateBytes on a rune boundary.
func secretsReportOf(names []string, state string) SecretsReport {
	if state == "" {
		return SecretsReport{}
	}
	kept := make([]string, 0, min(len(names), MaxSecretNames))
	for _, n := range names {
		if checkplugins.FormatSecretName.Valid(n) {
			kept = append(kept, n)
		}
	}
	slices.Sort(kept)
	kept = slices.Compact(kept)
	if len(kept) > MaxSecretNames {
		kept = kept[:MaxSecretNames:MaxSecretNames]
	}
	return SecretsReport{Known: true, Names: kept, State: boundLine(state, MaxSecretsStateBytes)}
}

// boundLine is secretsReportOf's state rule, and RecordConfigCheck's for
// the versions: valid UTF-8, control characters as spaces, at most maxBytes
// cut on a rune boundary.
func boundLine(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
