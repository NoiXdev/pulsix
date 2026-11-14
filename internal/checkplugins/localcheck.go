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
