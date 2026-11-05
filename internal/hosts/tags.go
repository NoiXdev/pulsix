package hosts

import (
	"context"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts/selector"
)

// tagKeyPattern is the brief's own tag-key rule: lowercase letters, digits,
// dots, underscores and hyphens, 1-64 characters — the same character class
// selector.Parse enforces for a selector's own keys (the two packages don't
// share the regexp, since a selector key and a tag key are validated by two
// different packages for two different reasons, but they describe the same
// shape on purpose: every tag key a host can actually carry must also be
// usable as a selector key).
var tagKeyPattern = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// maxTagValueLen and MaxManualTags are the brief's own tag limits: a value
// longer than maxTagValueLen, or a SetTags call naming more than
// MaxManualTags tags, is rejected with ErrInvalidInput rather than silently
// truncated or accepted.
const (
	maxTagValueLen = 256
	MaxManualTags  = 50
)

// TagSource names who or what set a tag: a person, through
// PUT /orgs/{org}/hosts/{id}/tags (TagSourceManual), or the monitoring
// agent, from a fact it reported (TagSourceFact — wired up by a later
// task). The two are stored as separate rows (see the host_tags migration)
// precisely so setting one never has to know about, or disturb, the other.
type TagSource string

const (
	TagSourceManual TagSource = "manual"
	TagSourceFact   TagSource = "fact"
)

// Tag is one key/value pair recorded on a host.
type Tag struct {
	Key    string
	Value  string
	Source TagSource
}

// TagValueCount is one (key, value) pair recorded somewhere in an
// organization, and how many hosts carry it — the flat shape
// Store.ListTagKeyValueCounts returns, which Service.ListTagKeys groups
// into []KeyValues for GET /orgs/{org}/tags.
type TagValueCount struct {
	Key   string
	Value string
	Count int
}

// KeyValues is every distinct value recorded for one tag key across an
// organization, each with how many hosts carry it — what the tag-selector
// editor's autocomplete actually renders (pick a key, see which values
// exist and how common each is).
type KeyValues struct {
	Key    string
	Values []ValueCount
}

// ValueCount is one value of a KeyValues' Key, and how many hosts carry it.
type ValueCount struct {
	Value string
	Count int
}

// validateTags reports ErrInvalidInput if tags cannot be accepted as a
// host's full manual tag set: too many tags, a key that does not match
// tagKeyPattern, a value longer than maxTagValueLen, or the same key named
// more than once (which would otherwise reach the store as two rows for
// the same (host_id, key, 'manual') primary key and fail as a generic
// unique-constraint error instead of a clear 400).
func validateTags(tags []Tag) error {
	if len(tags) > MaxManualTags {
		return fmt.Errorf("%w: at most %d manual tags per host, got %d", ErrInvalidInput, MaxManualTags, len(tags))
	}
	seen := make(map[string]bool, len(tags))
	var problems []string
	for _, t := range tags {
		if !tagKeyPattern.MatchString(t.Key) {
			problems = append(problems, fmt.Sprintf("key %q must match %s", t.Key, tagKeyPattern.String()))
			continue
		}
		if len(t.Value) > maxTagValueLen {
			problems = append(problems, fmt.Sprintf("value for key %q exceeds %d characters", t.Key, maxTagValueLen))
		}
		if seen[t.Key] {
			problems = append(problems, fmt.Sprintf("key %q is repeated", t.Key))
		}
		seen[t.Key] = true
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidInput, joinProblems(problems))
	}
	return nil
}

// joinProblems mirrors validateZoneInput's own "; "-joined problem list
// (service.go) — kept as a tiny local helper rather than a shared export
// since both call sites are internal to this package.
func joinProblems(problems []string) string {
	out := problems[0]
	for _, p := range problems[1:] {
		out += "; " + p
	}
	return out
}

// ListTags returns every tag — both TagSourceManual and TagSourceFact —
// recorded for the host identified by (orgID, hostID), or ErrNotFound if
// the host does not exist or belongs to a different organization.
func (s *Service) ListTags(ctx context.Context, orgID, hostID uuid.UUID) ([]Tag, error) {
	if _, err := s.store.GetHost(ctx, orgID, hostID); err != nil {
		return nil, err
	}
	return s.store.ListHostTags(ctx, orgID, hostID)
}

// SetTags replaces every manual tag on the host identified by (orgID,
// hostID) with tags; every TagSourceFact tag already on that host is left
// exactly as it was. Where a manual and a fact tag share a key, the manual
// one wins wherever the merged, effective value of that key is read — see
// Store.QueryHosts and selector.Selector.SQL's own doc comment for how the
// SQL-backed store gives that same precedence.
//
// tags is validated as a whole (validateTags) before anything is written:
// a call that would leave the host over MaxManualTags, or that names an
// invalid key or an over-long value, changes nothing at all rather than
// partially applying.
//
// The write bumps the host's revision and returns the new one (backlog
// 7.79), so a host edit opened before it answers ErrRevisionMismatch. A
// non-nil ifRevision is checked first: a stale one writes nothing and
// returns ErrRevisionMismatch; nil writes unconditionally. An unknown host
// is ErrNotFound either way.
func (s *Service) SetTags(ctx context.Context, orgID, hostID uuid.UUID, tags []Tag, ifRevision *int64) (int64, error) {
	if err := validateTags(tags); err != nil {
		return 0, err
	}
	manual := make([]Tag, len(tags))
	for i, t := range tags {
		manual[i] = Tag{Key: t.Key, Value: t.Value, Source: TagSourceManual}
	}
	return s.store.ReplaceManualTags(ctx, orgID, hostID, manual, ifRevision)
}

// ListTagKeys returns, for orgID, every tag key currently in use across its
// hosts, each with the values recorded for it and how many hosts carry
// each value — the data GET /orgs/{org}/tags exposes for the tag-selector
// editor's autocomplete.
func (s *Service) ListTagKeys(ctx context.Context, orgID uuid.UUID) ([]KeyValues, error) {
	counts, err := s.store.ListTagKeyValueCounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return groupKeyValues(counts), nil
}

// groupKeyValues folds a flat, (key, value)-ordered list of counts into one
// KeyValues per distinct key. It relies entirely on that ordering — every
// Store.ListTagKeyValueCounts implementation (the SQL store's ORDER BY key,
// value; the fake's own equivalent sort) must produce rows for the same key
// consecutively, or a key could be split across two KeyValues entries.
func groupKeyValues(counts []TagValueCount) []KeyValues {
	var out []KeyValues
	for _, c := range counts {
		if len(out) == 0 || out[len(out)-1].Key != c.Key {
			out = append(out, KeyValues{Key: c.Key})
		}
		last := &out[len(out)-1]
		last.Values = append(last.Values, ValueCount{Value: c.Value, Count: c.Count})
	}
	return out
}

// QueryHosts returns every host in orgID whose merged tags match sel — the
// endpoint the monitoring-group editor uses for live previews
// (POST /orgs/{org}/hosts/query).
func (s *Service) QueryHosts(ctx context.Context, orgID uuid.UUID, sel selector.Selector) ([]Host, error) {
	return s.store.QueryHosts(ctx, orgID, sel)
}
