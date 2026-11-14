package checkplugins

import "strings"

// LocalCheckPrefix starts the key of every check defined on the host, in
// the agent's agent.yaml (backlog 3.5a), so a local key never collides
// with a key the manager hands out.
const LocalCheckPrefix = "local-"

// ValidLocalCheckKey reports whether key is LocalCheckPrefix followed by at
// least one character, the whole key having CheckKeyPattern's shape. It
// does not trim.
func ValidLocalCheckKey(key string) bool {
	return len(key) > len(LocalCheckPrefix) && strings.HasPrefix(key, LocalCheckPrefix) && ValidCheckKey(key)
}

// The bounds of the checks section that both sides enforce: the agent keeps
// to them when it reads its agent.yaml, and the manager holds every
// heartbeat to them again. They live here so that neither side can change
// one alone and silently drop what the other still sends.
const (
	// MaxLocalChecks is how many local checks an agent runs and the
	// manager keeps, the first in key order.
	MaxLocalChecks = 32
	// MaxLocalCheckParamsBytes bounds one local check's params as canonical
	// JSON (CanonicalParams), as reported and as stored.
	MaxLocalCheckParamsBytes = 2048
	// MaxLocalCheckProblems is how many problems with local checks a
	// heartbeat carries and the manager keeps, in the order sent.
	MaxLocalCheckProblems = 32
)
