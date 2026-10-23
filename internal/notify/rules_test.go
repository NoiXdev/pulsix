package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// fakeRuleStore backs TestOrgScheduleCategoriesUnionsAcrossChannels: a
// minimal RuleStore that only needs to answer OrgScheduleCategories, since
// that is the one Service method under test here. rows is keyed by
// schedule id, exactly what the real store returns (one org per call).
type fakeRuleStore struct {
	rows map[uuid.UUID][][]string
}

func (f fakeRuleStore) ScheduleRules(context.Context, uuid.UUID, uuid.UUID) ([]ChannelRule, error) {
	return nil, nil
}
func (f fakeRuleStore) ReplaceScheduleRules(context.Context, uuid.UUID, uuid.UUID, []ChannelRule) error {
	return nil
}
func (f fakeRuleStore) OrgScheduleCategories(context.Context, uuid.UUID) (map[uuid.UUID][][]string, error) {
	return f.rows, nil
}

func TestMergeAndSplitRules(t *testing.T) {
	a, b, c := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	merged, err := mergeRules([]Rule{
		{On: CategoryChange, Channels: []uuid.UUID{b}},
		{On: CategoryFailure, Channels: []uuid.UUID{a, b}},
		{On: CategorySkipped, Channels: []uuid.UUID{c}},
		{On: CategoryFailure, Channels: []uuid.UUID{a}}, // repeated: merged away
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 3 || merged[0].ChannelID != b || len(merged[0].Events) != 2 ||
		merged[0].Events[0] != CategoryFailure || merged[0].Events[1] != CategoryChange {
		t.Fatalf("merged = %+v", merged)
	}
	merged[0].ChannelName, merged[1].ChannelName, merged[2].ChannelName = "b", "a", "c"
	views := splitRules(merged)
	if len(views) != 3 || views[0].On != CategoryFailure || len(views[0].Channels) != 2 ||
		views[1].On != CategoryChange || views[1].Channels[0].Name != "b" || views[2].On != CategorySkipped {
		t.Fatalf("views = %+v", views)
	}
	for _, bad := range [][]Rule{
		{{On: "always", Channels: []uuid.UUID{a}}},
		{{On: CategoryFailure}},
		make([]Rule, maxRules+1),
	} {
		if _, err := mergeRules(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("mergeRules(%v) = %v", bad, err)
		}
	}
	if got := splitRules(nil); got != nil {
		t.Fatalf("no rules = %+v", got)
	}
}

// TestMergeRulesDeduplicatesCategories covers the carry-over from the
// Task 2 review: schedule_notifications.events must never hold a category
// twice, which a Postgres CHECK cannot express — the merge is what
// guarantees it, whether a rule lists the same channel twice or two rules
// name the same (channel, category) pair. splitRules survives a stored row
// that does hold a duplicate (a hand-edited row) without listing the
// channel twice.
func TestMergeRulesDeduplicatesCategories(t *testing.T) {
	a := uuid.Must(uuid.NewV7())
	merged, err := mergeRules([]Rule{
		{On: CategoryFailure, Channels: []uuid.UUID{a, a}},
		{On: CategoryFailure, Channels: []uuid.UUID{a}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || len(merged[0].Events) != 1 || merged[0].Events[0] != CategoryFailure {
		t.Fatalf("merged = %+v, want one channel listening to failure once", merged)
	}
	views := splitRules([]ChannelRule{{ChannelID: a, ChannelName: "a", Events: []Category{CategoryFailure, CategoryFailure}}})
	if len(views) != 1 || len(views[0].Channels) != 1 {
		t.Fatalf("views = %+v, want the channel named once", views)
	}
}

// TestOrgScheduleCategoriesUnionsAcrossChannels (2.73): a schedule with
// several channels, each listening to different categories, must report
// the union of all of them, in canonical order and without duplicates — a
// schedule with no rows at all must not appear in the result (so the
// schedules list can treat "absent" as "no notify rules", matching what
// json:"notify_on,omitempty" already does for an empty slice).
func TestOrgScheduleCategoriesUnionsAcrossChannels(t *testing.T) {
	org := uuid.Must(uuid.NewV7())
	withRules, noRules := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	svc := NewService(Deps{Rules: fakeRuleStore{rows: map[uuid.UUID][][]string{
		withRules: {
			{"failure", "change"},
			{"change", "skipped"}, // "change" repeated across channels
		},
	}}})
	got, err := svc.OrgScheduleCategories(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	want := []Category{CategoryFailure, CategoryChange, CategorySkipped}
	if cats := got[withRules]; len(cats) != len(want) {
		t.Fatalf("categories = %+v, want %+v", cats, want)
	} else {
		for i, c := range want {
			if cats[i] != c {
				t.Fatalf("categories = %+v, want %+v (canonical order)", cats, want)
			}
		}
	}
	if _, ok := got[noRules]; ok {
		t.Fatalf("a schedule with no rows must not appear in the result: %+v", got)
	}
}

// TestMergeRulesBoundsChannelsPerRule is backlog 2.82. The number of
// RULES was bounded in the service (maxRules), but the channels inside
// one rule only by the HTTP layer's maxItems tag — so the bound lived
// in the transport, and any other caller of SetScheduleRules could set
// a rule naming thousands of channels, each of which becomes a stored
// row and a delivery per event. The service owns its own rules.
func TestMergeRulesBoundsChannelsPerRule(t *testing.T) {
	channels := make([]uuid.UUID, maxRuleChannels+1)
	for i := range channels {
		channels[i] = uuid.Must(uuid.NewV7())
	}
	if _, err := mergeRules([]Rule{{On: CategoryFailure, Channels: channels}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mergeRules with %d channels = %v, want ErrInvalidInput", len(channels), err)
	}
	if _, err := mergeRules([]Rule{{On: CategoryFailure, Channels: channels[:maxRuleChannels]}}); err != nil {
		t.Fatalf("mergeRules with exactly %d channels = %v, want it accepted", maxRuleChannels, err)
	}
	// The bound is per rule, not across the whole set: two rules may
	// each name a full list, and the same channel in both is one
	// stored row.
	merged, err := mergeRules([]Rule{
		{On: CategoryFailure, Channels: channels[:maxRuleChannels]},
		{On: CategoryChange, Channels: channels[:maxRuleChannels]},
	})
	if err != nil {
		t.Fatalf("two full rules = %v, want them accepted", err)
	}
	if len(merged) != maxRuleChannels {
		t.Fatalf("merged = %d channel rules, want %d", len(merged), maxRuleChannels)
	}
}
