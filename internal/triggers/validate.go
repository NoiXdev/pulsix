package triggers

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/ansiblevars"
	"github.com/noixdev/pulsix/internal/clientip"
	"github.com/noixdev/pulsix/internal/templates"
)

const (
	maxNameRunes = 100
	maxCIDRs     = 50
)

var headerNameRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// DefaultRateLimit is the spec's 10 per minute.
var DefaultRateLimit = RateLimit{Count: 10, Per: "1m"}

func normalizeName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > maxNameRunes {
		return "", fmt.Errorf("%w: name must be 1-%d characters", ErrInvalidInput, maxNameRunes)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: name must not contain control characters", ErrInvalidInput)
		}
	}
	return n, nil
}

func parseCIDRs(raw []string) ([]netip.Prefix, error) {
	if len(raw) > maxCIDRs {
		return nil, fmt.Errorf("%w: at most %d allowlist entries", ErrInvalidInput, maxCIDRs)
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, r := range raw {
		p, err := clientip.ParsePrefixes(r)
		if err != nil || len(p) != 1 {
			return nil, fmt.Errorf("%w: allowlist entry %q is not a CIDR or address", ErrInvalidInput, r)
		}
		out = append(out, p[0])
	}
	return out, nil
}

func normalizeRateLimit(rl RateLimit) (RateLimit, error) {
	if rl == (RateLimit{}) {
		return DefaultRateLimit, nil
	}
	per, err := time.ParseDuration(rl.Per)
	if err != nil || per < time.Second || per > 24*time.Hour || rl.Count < 1 || rl.Count > 1000 {
		return rl, fmt.Errorf("%w: rate limit is 1-1000 calls per 1s-24h", ErrInvalidInput)
	}
	return rl, nil
}

// capacityAndRefill turns a limit into bucket parameters.
func capacityAndRefill(rl RateLimit) (capacity, perSecond float64) {
	per, err := time.ParseDuration(rl.Per)
	if err != nil || per <= 0 {
		per = time.Minute
	}
	return float64(rl.Count), float64(rl.Count) / per.Seconds()
}

// checkMappingTargets refuses targets a payload may not set: the 2.36
// denylist, secret fields, and — when the template has a schema — names
// it does not declare.
func checkMappingTargets(m Mapping, t templates.Template) error {
	secret := templates.SecretFields(t.ExtraVarsSchema)
	declared := map[string]bool{}
	if t.ExtraVarsSchema != nil {
		for _, f := range t.ExtraVarsSchema.Fields {
			declared[f.Name] = true
		}
	}
	for name := range m.ExtraVars {
		switch {
		case ansiblevars.Forbidden(name):
			return fmt.Errorf("%w: %s controls connection security and cannot be mapped", ErrInvalidInput, name)
		case secret[name]:
			return fmt.Errorf("%w: %s is a secret field and cannot be set from a payload", ErrInvalidInput, name)
		case t.ExtraVarsSchema != nil && !declared[name]:
			return fmt.Errorf("%w: %s is not declared in the template's extra vars schema", ErrInvalidInput, name)
		}
	}
	return nil
}
