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

// The bounds of a request's own expiry in minutes (spec 2026-11-25 A,
// "Expiry"); TTL is the default.
const (
	MinExpiresInMinutes = 5
	MaxExpiresInMinutes = 24 * 60
)

// Overrides is what a run request changes about its template's run (spec
// 2026-11-25 assistant API gaps, A): a dry run (Check, with Diff), extra
// vars that replace the template's top-level ones, the hosts the run is
// narrowed to and a limit that narrows the template's own. They are stored
// on the request as they were asked for and never change after; the
// picture shows them and the approval enqueues them (Resolver.Run). They
// are an assistant's input and treated as such: extra vars are never
// secret, never an Ansible marker or a Pulsix secret marker, and reach the
// run marked unsafe for Ansible; the limit is a plain host pattern that
// is resolved into the hosts the run keeps to and never reaches Ansible.
// Numbers in ExtraVars are json.Number, so they stay exact.
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

// expiresIn is the request's time to live: TTL when minutes is 0, else
// between MinExpiresInMinutes and MaxExpiresInMinutes, checked before the
// conversion so no value can overflow into a valid one.
func expiresIn(minutes int) (time.Duration, error) {
	if minutes == 0 {
		return TTL, nil
	}
	if minutes < MinExpiresInMinutes || minutes > MaxExpiresInMinutes {
		return 0, fmt.Errorf("%w: expires_in must be between %d and %d minutes", ErrInvalidInput, MinExpiresInMinutes, MaxExpiresInMinutes)
	}
	return time.Duration(minutes) * time.Minute, nil
}
