package checkplugins

import "regexp"

// CheckKeyPattern is the shape of a check's key: the name its definition
// is stored under, its samples and states are keyed by, and an item-scoped
// alert rule names (backlog 3.13e). Every plugin name satisfies it, so a
// key left to default to the plugin's name never fails it. It lives in
// this leaf package so internal/checks (which validates definitions) and
// internal/rules (which validates rules, and never imports internal/checks)
// share one definition.
const CheckKeyPattern = `^[a-z0-9][a-z0-9-]{0,62}$`

var checkKeyRE = regexp.MustCompile(CheckKeyPattern)

// ValidCheckKey reports whether key has CheckKeyPattern's shape. It does
// not trim: callers normalise first.
func ValidCheckKey(key string) bool { return checkKeyRE.MatchString(key) }
