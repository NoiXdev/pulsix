package runrequests

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxHostIDs caps the hosts one run request narrows its run to.
const MaxHostIDs = 500

// The bounds of a request's own expiry (spec 2026-11-25 A, "Expiry"); TTL is
// the default.
const (
	MinExpiresIn = 5 * time.Minute
	MaxExpiresIn = 24 * time.Hour
)

// Overrides is what a run request changes about its template's run (spec
// 2026-11-25 assistant API gaps, A): a dry run (Check, with Diff), extra
// vars that replace the template's top-level ones, the hosts the run is
// narrowed to and a limit that narrows the template's own. They are stored
// on the request as they were asked for and never change after; the
// picture shows them and the approval enqueues them (Resolver.Run). Extra
// vars are never secret: a secret key is refused when the request is made
// and again at the approval.
type Overrides struct {
	Check     bool           `json:"check,omitempty"`
	Diff      bool           `json:"diff,omitempty"`
	ExtraVars map[string]any `json:"extra_vars,omitempty"`
	// HostIDs is sorted and holds every host once.
	HostIDs []uuid.UUID `json:"host_ids,omitempty"`
	Limit   *string     `json:"limit,omitempty"`
}

// OverridesInput is the overrides as a caller asks for them.
type OverridesInput struct {
	CheckMode bool
	// Diff nil follows CheckMode: a dry run shows its diff unless asked not to.
	Diff      *bool
	ExtraVars map[string]any
	HostIDs   []uuid.UUID
	// Limit empty (or only blanks) is no limit override.
	Limit *string
}

// overrides normalises in: nil when it changes nothing. Diff without check
// mode and more than MaxHostIDs hosts are refused.
func (in OverridesInput) overrides() (*Overrides, error) {
	out := Overrides{Check: in.CheckMode, Diff: in.CheckMode}
	if in.Diff != nil {
		out.Diff = *in.Diff
	}
	if out.Diff && !out.Check {
		return nil, fmt.Errorf("%w: diff only applies to a dry run (check_mode)", ErrInvalidInput)
	}
	if len(in.ExtraVars) > 0 {
		out.ExtraVars = maps.Clone(in.ExtraVars)
	}
	if in.Limit != nil {
		if l := strings.TrimSpace(*in.Limit); l != "" {
			out.Limit = &l
		}
	}
	if len(in.HostIDs) > 0 {
		ids := slices.Clone(in.HostIDs)
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return cmp.Compare(a.String(), b.String()) })
		out.HostIDs = slices.Compact(ids)
		if len(out.HostIDs) > MaxHostIDs {
			return nil, fmt.Errorf("%w: at most %d host_ids", ErrInvalidInput, MaxHostIDs)
		}
	}
	if !out.Check && len(out.ExtraVars) == 0 && out.Limit == nil && len(out.HostIDs) == 0 {
		return nil, nil
	}
	return &out, nil
}

// expiresIn is the request's time to live: TTL when not given, else between
// MinExpiresIn and MaxExpiresIn.
func expiresIn(d time.Duration) (time.Duration, error) {
	if d == 0 {
		return TTL, nil
	}
	if d < MinExpiresIn || d > MaxExpiresIn {
		return 0, fmt.Errorf("%w: expires_in must be between %d and %d minutes", ErrInvalidInput,
			int(MinExpiresIn/time.Minute), int(MaxExpiresIn/time.Minute))
	}
	return d, nil
}

// runLimit is the limit a run request's run ships when the request has a
// limit override: the override alone when the template has no limit, else
// the template's limit intersected with it, so an override only ever
// narrows. Ansible unions a pattern's plain terms and then intersects the
// "&" terms and removes the "!" terms, so the intersection is exact only
// for an override of one term: with a template limit, an override of
// several terms is refused. The template's terms keep their separator (a
// pattern holding a comma splits on commas only, see jobs.narrowLimit).
func runLimit(tmplLimit, override string) (string, error) {
	if tmplLimit == "" {
		return override, nil
	}
	if strings.ContainsAny(override, ":,") {
		return "", fmt.Errorf("%w: the template has its own limit (%s); a limit override narrows it and may name one pattern only (a host, a group or a glob, or one !exclusion)",
			ErrInvalidInput, tmplLimit)
	}
	term := override
	if !strings.HasPrefix(term, "!") && !strings.HasPrefix(term, "&") {
		term = "&" + term
	}
	sep := ":"
	if strings.Contains(tmplLimit, ",") {
		sep = ","
	}
	return tmplLimit + sep + term, nil
}
