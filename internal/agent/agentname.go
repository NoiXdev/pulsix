package agent

import "strings"

// maxAgentNameBytes is the longest host name internal/hosts accepts.
const maxAgentNameBytes = 253

// DefaultDataDir is the agent's data directory when PULSIX_DATA_DIR is
// unset: each system's usual place. programData is the ProgramData
// environment value (Windows only). The Windows path is joined with a
// backslash explicitly so the result does not depend on the host the code
// runs on.
func DefaultDataDir(goos, programData string) string {
	switch goos {
	case "darwin":
		return "/Library/Application Support/Pulsix"
	case "windows":
		if programData == "" {
			programData = `C:\ProgramData`
		}
		return strings.TrimRight(programData, `\`) + `\Pulsix`
	default:
		return "/etc/pulsix"
	}
}

// normalizeAgentName turns an OS hostname into a valid host name
// (^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?$): lower-case, a trailing ".local"
// and a trailing dot stripped, every other character outside [a-z0-9.-]
// replaced by one '-' per rune, then leading and trailing '-' and '.'
// trimmed. ok is false when nothing usable is left or the result is longer
// than 253 bytes.
func normalizeAgentName(hostname string) (string, bool) {
	s := strings.ToLower(hostname)
	s = strings.TrimSuffix(s, ".local")
	s = strings.TrimSuffix(s, ".")
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return '-'
	}, s)
	s = strings.Trim(s, "-.")
	if s == "" || len(s) > maxAgentNameBytes {
		return "", false
	}
	return s, true
}
