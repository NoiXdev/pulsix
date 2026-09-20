package notify

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

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
