package notify

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// Rule is one entry of a schedule's notify_rules (spec §7): the channels
// told when a run or decision falls into the category.
type Rule struct {
	On       Category
	Channels []uuid.UUID
}

// ChannelRule is the stored, merged form: every category one channel is
// told about. One row per (schedule, channel) is what makes "at most one
// message per run and channel" hold by construction.
type ChannelRule struct {
	ChannelID   uuid.UUID
	ChannelName string
	ChannelKind Kind
	Events      []Category
}

// RuleView is a rule with its channels' names, for reading.
type RuleView struct {
	On       Category
	Channels []ChannelRef
}

// ChannelRef names a channel.
type ChannelRef struct {
	ID   uuid.UUID
	Name string
	Kind Kind
}

// RuleStore persists schedule rules (org-scoped).
type RuleStore interface {
	ScheduleRules(ctx context.Context, orgID, scheduleID uuid.UUID) ([]ChannelRule, error)
	ReplaceScheduleRules(ctx context.Context, orgID, scheduleID uuid.UUID, rules []ChannelRule) error
	// OrgScheduleCategories lists every schedule of the organization that
	// has at least one notify rule, each with the raw per-channel event
	// categories it stores (one []string per channel row — a schedule can
	// have several). OrgScheduleCategories (the Service method) unions
	// these per schedule; the store stays a plain read.
	OrgScheduleCategories(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][][]string, error)
}

const maxRules = 16

// maxRuleChannels bounds the channels ONE rule may name, matching the
// digest's own maxDigestChannels. The bound used to exist only as the
// HTTP layer's maxItems tag (backlog 2.82), which left it in the
// transport: every other caller of SetScheduleRules could name
// thousands of channels, each a stored row and a delivery per matching
// event. It is per rule, not across the set — naming the same full
// list for failures and for changes is one stored row per channel.
const maxRuleChannels = 20

// mergeRules folds per-category rules into one entry per channel, in the
// order channels first appear; categories are kept in canonical order.
func mergeRules(rules []Rule) ([]ChannelRule, error) {
	if len(rules) > maxRules {
		return nil, fmt.Errorf("%w: at most %d rules", ErrInvalidInput, maxRules)
	}
	byChannel := map[uuid.UUID]map[Category]bool{}
	var order []uuid.UUID
	for _, r := range rules {
		if _, ok := ParseCategory(string(r.On)); !ok {
			return nil, fmt.Errorf("%w: a rule's on must be failure, change, success or skipped", ErrInvalidInput)
		}
		if len(r.Channels) == 0 {
			return nil, fmt.Errorf("%w: the %s rule names no channel", ErrInvalidInput, r.On)
		}
		if len(r.Channels) > maxRuleChannels {
			return nil, fmt.Errorf("%w: the %s rule names %d channels; at most %d", ErrInvalidInput, r.On, len(r.Channels), maxRuleChannels)
		}
		for _, ch := range r.Channels {
			if byChannel[ch] == nil {
				byChannel[ch] = map[Category]bool{}
				order = append(order, ch)
			}
			byChannel[ch][r.On] = true
		}
	}
	out := make([]ChannelRule, 0, len(order))
	for _, ch := range order {
		cr := ChannelRule{ChannelID: ch}
		for _, c := range Categories() {
			if byChannel[ch][c] {
				cr.Events = append(cr.Events, c)
			}
		}
		out = append(out, cr)
	}
	return out, nil
}

// splitRules turns the stored per-channel form back into per-category
// rules, in canonical category order; categories nobody listens to are
// left out.
func splitRules(stored []ChannelRule) []RuleView {
	var out []RuleView
	for _, c := range Categories() {
		v := RuleView{On: c}
		for _, cr := range stored {
			if slices.Contains(cr.Events, c) {
				v.Channels = append(v.Channels, ChannelRef{ID: cr.ChannelID, Name: cr.ChannelName, Kind: cr.ChannelKind})
			}
		}
		if len(v.Channels) > 0 {
			out = append(out, v)
		}
	}
	return out
}

// ScheduleRules reads a schedule's rules. The caller has checked the
// schedule exists and may be read.
func (s *Service) ScheduleRules(ctx context.Context, orgID, scheduleID uuid.UUID) ([]RuleView, error) {
	stored, err := s.d.Rules.ScheduleRules(ctx, orgID, scheduleID)
	if err != nil {
		return nil, err
	}
	return splitRules(stored), nil
}

// OrgScheduleCategories reports, for every schedule of the organization
// that has at least one notify rule, the distinct categories any of its
// channels are told about — in canonical order, the same order
// splitRules keeps rules in. It is the schedules list's "Notify" column
// (backlog 2.73): one call for the whole list rather than one per
// schedule row.
func (s *Service) OrgScheduleCategories(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID][]Category, error) {
	raw, err := s.d.Rules.OrgScheduleCategories(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]Category, len(raw))
	for scheduleID, rows := range raw {
		have := map[Category]bool{}
		for _, events := range rows {
			for _, e := range events {
				if c, ok := ParseCategory(e); ok {
					have[c] = true
				}
			}
		}
		var cats []Category
		for _, c := range Categories() {
			if have[c] {
				cats = append(cats, c)
			}
		}
		out[scheduleID] = cats
	}
	return out, nil
}

// SetScheduleRules replaces a schedule's rules and returns them as
// stored. An unknown channel is ErrInvalidInput.
func (s *Service) SetScheduleRules(ctx context.Context, orgID, scheduleID uuid.UUID, rules []Rule) ([]RuleView, error) {
	merged, err := mergeRules(rules)
	if err != nil {
		return nil, err
	}
	if err := s.d.Rules.ReplaceScheduleRules(ctx, orgID, scheduleID, merged); err != nil {
		return nil, err
	}
	return s.ScheduleRules(ctx, orgID, scheduleID)
}
